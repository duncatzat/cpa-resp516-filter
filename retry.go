package main

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Wire types for host.model.execute
// (field names match pluginapi.HostModelExecutionRequest — has json tags)
// ---------------------------------------------------------------------------

type hostModelExecutionRequest struct {
	EntryProtocol string              `json:"entry_protocol"`
	ExitProtocol  string              `json:"exit_protocol"`
	Model         string              `json:"model"`
	Stream        bool                `json:"stream"`
	Body          []byte              `json:"body"`
	Headers       map[string][]string `json:"headers"`
	Query         map[string][]string `json:"query"`
	Alt           string              `json:"alt"`
}

type hostModelExecutionResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

// ---------------------------------------------------------------------------
// Retry logic — re-execute the original request through host.model.execute
// ---------------------------------------------------------------------------

// doRetry re-executes the original request through the host model callback
// up to rule.retryMax times. Each retry response is checked for the same
// degradation signature. Returns the first non-degraded response body, or
// (nil, false) if all retries are exhausted.
func doRetry(rule *compiledRule, req responseTransformRequest, detectedTokens int64, detectedPath string) ([]byte, bool) {
	cfg := getConfig()
	if cfg == nil {
		return nil, false
	}

	stats := getRuleStats(rule.name)

	for attempt := 1; attempt <= rule.retryMax; attempt++ {
		applyBackoff(rule.retryBackoff)

		stats.retries.Add(1)

		execReq := hostModelExecutionRequest{
			EntryProtocol: req.FromFormat,
			ExitProtocol:   req.ToFormat,
			Model:          req.Model,
			Stream:         false,
			Body:           req.OriginalRequest,
		}

		payload, err := json.Marshal(execReq)
		if err != nil {
			continue
		}

		respRaw, err := callHost("host.model.execute", payload)
		if err != nil || len(respRaw) == 0 {
			continue
		}

		var env envelope
		if err := json.Unmarshal(respRaw, &env); err != nil || !env.OK {
			continue
		}

		var execResp hostModelExecutionResponse
		if err := json.Unmarshal(env.Result, &execResp); err != nil {
			continue
		}

		if execResp.StatusCode >= 400 || len(execResp.Body) == 0 {
			continue
		}

		// Check if the retry response is also degraded.
		retryTokens, retryPath := extractReasoningTokens(execResp.Body, rule.paths)
		if retryTokens >= 0 && matchOperator(rule.operator, rule.values, retryTokens) {
			renderLog(rule, tplCtx{
				Rule:    rule.name,
				Model:   req.Model,
				Tokens:  retryTokens,
				Path:    retryPath,
				Attempt: attempt,
			})
			continue
		}

		// Success — non-degraded response.
		renderLog(rule, tplCtx{
			Rule:    rule.name,
			Model:   req.Model,
			Tokens:  retryTokens,
			Path:    retryPath,
			Attempt: attempt,
		})

		return execResp.Body, true
	}

	// All retries exhausted.
	renderLog(rule, tplCtx{
		Rule:    rule.name,
		Model:   req.Model,
		Tokens:  detectedTokens,
		Path:    detectedPath,
		Attempt: rule.retryMax,
	})

	return nil, false
}
