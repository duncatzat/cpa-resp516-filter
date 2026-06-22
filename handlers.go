package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
	"time"
)

// ---------------------------------------------------------------------------
// Wire types for response.normalize_after
// (field names match pluginapi.ResponseTransformRequest — no json tags)
// ---------------------------------------------------------------------------

type responseTransformRequest struct {
	FromFormat        string `json:"FromFormat"`
	ToFormat          string `json:"ToFormat"`
	Model             string `json:"Model"`
	Stream            bool   `json:"Stream"`
	OriginalRequest   []byte `json:"OriginalRequest"`
	TranslatedRequest []byte `json:"TranslatedRequest"`
	Body              []byte `json:"Body"`
}

type payloadResponse struct {
	Body []byte `json:"Body"`
}

// ---------------------------------------------------------------------------
// Wire types for response.intercept_stream_chunk
// (field names match pluginapi.StreamChunkInterceptRequest — no json tags)
// ---------------------------------------------------------------------------

type streamChunkInterceptRequest struct {
	SourceFormat    string              `json:"SourceFormat"`
	Model           string              `json:"Model"`
	RequestedModel  string              `json:"RequestedModel"`
	Stream          bool                `json:"Stream"`
	RequestHeaders  map[string][]string `json:"RequestHeaders"`
	ResponseHeaders map[string][]string `json:"ResponseHeaders"`
	OriginalRequest []byte              `json:"OriginalRequest"`
	RequestBody     []byte              `json:"RequestBody"`
	Body            []byte              `json:"Body"`
	HistoryChunks   [][]byte            `json:"HistoryChunks"`
	ChunkIndex      int                 `json:"ChunkIndex"`
	Metadata        map[string]any      `json:"Metadata"`
}

type streamChunkInterceptResponse struct {
	Headers      map[string][]string `json:"Headers,omitempty"`
	Body         []byte              `json:"Body,omitempty"`
	ClearHeaders []string            `json:"ClearHeaders,omitempty"`
	DropChunk    bool                `json:"DropChunk,omitempty"`
}

// ---------------------------------------------------------------------------
// Wire types for usage.handle
// (field names match pluginapi.UsageRecord — no json tags)
// ---------------------------------------------------------------------------

type usageRecord struct {
	Provider         string      `json:"Provider"`
	ExecutorType     string      `json:"ExecutorType"`
	Model            string      `json:"Model"`
	Alias            string      `json:"Alias"`
	AuthID           string      `json:"AuthID"`
	Source           string      `json:"Source"`
	ReasoningEffort  string      `json:"ReasoningEffort"`
	ServiceTier      string      `json:"ServiceTier"`
	Failed           bool        `json:"Failed"`
	Detail           usageDetail `json:"Detail"`
}

type usageDetail struct {
	InputTokens     int64 `json:"InputTokens"`
	OutputTokens    int64 `json:"OutputTokens"`
	ReasoningTokens int64 `json:"ReasoningTokens"`
	CachedTokens    int64 `json:"CachedTokens"`
	TotalTokens     int64 `json:"TotalTokens"`
}

// ---------------------------------------------------------------------------
// Handler: plugin.register / plugin.reconfigure
// ---------------------------------------------------------------------------

func handleRegister(request []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(request) > 0 {
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, err
		}
	}
	if err := parseConfig(req.ConfigYAML); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}
	reg := buildRegistration()
	result, err := json.Marshal(reg)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

// ---------------------------------------------------------------------------
// Handler: response.normalize_after (non-streaming)
// ---------------------------------------------------------------------------

func handleResponseNormalizeAfter(raw []byte) ([]byte, error) {
	var req responseTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	body := req.Body
	cfg := getConfig()
	if cfg == nil || !cfg.enabled || len(body) == 0 {
		return okEnvelope(payloadResponse{Body: body})
	}

	rule, tokens, path := matchRules(cfg, req.Model, body, nil, false)
	if rule == nil {
		return okEnvelope(payloadResponse{Body: body})
	}

	stats := getRuleStats(rule.name)
	stats.matches.Add(1)
	renderLog(rule, tplCtx{Rule: rule.name, Model: req.Model, Tokens: tokens, Path: path})

	switch rule.action {
	case "retry":
		retryBody, ok := doRetry(rule, req, tokens, path)
		if ok {
			stats.retrySuccesses.Add(1)
			return okEnvelope(payloadResponse{Body: retryBody})
		}
		// Retry exhausted — fall through to error.
		fallthrough
	case "error":
		errBody := renderTemplate(rule.errorBody, tplCtx{Rule: rule.name, Model: req.Model, Tokens: tokens, Path: path})
		return okEnvelope(payloadResponse{Body: errBody})
	default:
		return okEnvelope(payloadResponse{Body: body})
	}
}

// ---------------------------------------------------------------------------
// Handler: response.intercept_stream_chunk (streaming)
// ---------------------------------------------------------------------------

func handleStreamChunk(raw []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	// Header-only init call (ChunkIndex == -1): no body to inspect.
	if req.ChunkIndex < 0 {
		return okEnvelope(streamChunkInterceptResponse{})
	}

	chunk := req.Body
	cfg := getConfig()
	if cfg == nil || !cfg.enabled || len(chunk) == 0 {
		return okEnvelope(streamChunkInterceptResponse{})
	}

	// Fast pre-filter: skip chunks that don't contain any keyword.
	if !chunkContainsKeyword(chunk, cfg.streamKeywords) {
		return okEnvelope(streamChunkInterceptResponse{})
	}

	rule, tokens, path := matchRules(cfg, req.Model, chunk, nil, true)
	if rule == nil {
		return okEnvelope(streamChunkInterceptResponse{})
	}

	stats := getRuleStats(rule.name)
	stats.matches.Add(1)
	renderLog(rule, tplCtx{Rule: rule.name, Model: req.Model, Tokens: tokens, Path: path})

	switch rule.action {
	case "error", "retry":
		// Streaming retry is not feasible mid-stream (prior chunks already
		// delivered). Inject an error SSE event so the client can retry the
		// full request.
		errChunk := renderTemplate(rule.streamError, tplCtx{Rule: rule.name, Model: req.Model, Tokens: tokens, Path: path})
		return okEnvelope(streamChunkInterceptResponse{Body: errChunk})
	default:
		return okEnvelope(streamChunkInterceptResponse{})
	}
}

// ---------------------------------------------------------------------------
// Handler: usage.handle
// ---------------------------------------------------------------------------

func handleUsage(raw []byte) ([]byte, error) {
	var record usageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}

	cfg := getConfig()
	if cfg == nil || !cfg.enabled {
		return okEnvelope(map[string]any{})
	}

	// Check if any rule matches this usage record's reasoning tokens.
	for i := range cfg.rules {
		rule := &cfg.rules[i]
		if !modelMatches(rule.models, record.Model, cfg.modelMatching) {
			continue
		}
		if matchOperator(rule.operator, rule.values, record.Detail.ReasoningTokens) {
			stats := getRuleStats(rule.name)
			stats.matches.Add(1)
			renderLog(rule, tplCtx{Rule: rule.name, Model: record.Model, Tokens: record.Detail.ReasoningTokens})
			break
		}
	}

	return okEnvelope(map[string]any{})
}

// ---------------------------------------------------------------------------
// Rule matching engine
// ---------------------------------------------------------------------------

// matchRules evaluates rules in order. Returns the first matching rule, the
// detected token value, and the JSON path where it was found.
// For streaming responses, isStream=true and stream-specific paths are
// included in the search.
func matchRules(cfg *compiledConfig, model string, data []byte, _ []byte, isStream bool) (*compiledRule, int64, string) {
	for i := range cfg.rules {
		rule := &cfg.rules[i]
		if !modelMatches(rule.models, model, cfg.modelMatching) {
			continue
		}
		var tokens int64
		var path string
		if isStream {
			tokens, path = extractReasoningTokensFromChunk(data, rule.paths, rule.streamPaths)
		} else {
			tokens, path = extractReasoningTokens(data, rule.paths)
		}
		if tokens < 0 {
			continue
		}
		if matchOperator(rule.operator, rule.values, tokens) {
			return rule, tokens, path
		}
	}
	return nil, -1, ""
}

// ---------------------------------------------------------------------------
// Template rendering
// ---------------------------------------------------------------------------

func renderTemplate(tpl *template.Template, ctx tplCtx) []byte {
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, ctx); err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":"respfilter: template error: %s","type":"degradation_filter"}}`, err.Error()))
	}
	return buf.Bytes()
}

func renderLog(rule *compiledRule, ctx tplCtx) {
	if !rule.log {
		return
	}
	var buf bytes.Buffer
	if err := rule.logMsg.Execute(&buf, ctx); err != nil {
		return
	}
	logHost(rule.logLevel, buf.String())
}

// ---------------------------------------------------------------------------
// Backoff helper
// ---------------------------------------------------------------------------

func applyBackoff(ms int) {
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
