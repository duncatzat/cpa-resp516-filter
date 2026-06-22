package main

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// YAML config types (mapped from plugins.configs.respfilter in config.yaml)
// ---------------------------------------------------------------------------

type yamlConfig struct {
	Enabled       bool         `yaml:"enabled"`
	Rules         []yamlRule   `yaml:"rules"`
	Defaults      yamlDefaults `yaml:"defaults"`
	Stream        yamlStream   `yaml:"stream"`
	ModelMatching string       `yaml:"model_matching"`
}

type yamlRule struct {
	Name          string   `yaml:"name"`
	Paths         []string `yaml:"paths"`
	StreamPaths   []string `yaml:"stream_paths"`
	Operator      string   `yaml:"operator"`
	Values        []int64  `yaml:"values"`
	Models        []string `yaml:"models"`
	Action        string   `yaml:"action"`
	ErrorStatus   int      `yaml:"error_status"`
	ErrorBody     string   `yaml:"error_body"`
	StreamError   string   `yaml:"stream_error"`
	RetryMax      int      `yaml:"retry_max"`
	RetryBackoff  int      `yaml:"retry_backoff_ms"`
	Log           *bool    `yaml:"log"`
	LogLevel      string   `yaml:"log_level"`
	LogMsg        string   `yaml:"log_msg"`
}

type yamlDefaults struct {
	Paths        []string `yaml:"paths"`
	StreamPaths  []string `yaml:"stream_paths"`
	Operator     string   `yaml:"operator"`
	Values       []int64  `yaml:"values"`
	Models       []string `yaml:"models"`
	Action       string   `yaml:"action"`
	ErrorStatus  int      `yaml:"error_status"`
	ErrorBody    string   `yaml:"error_body"`
	StreamError  string   `yaml:"stream_error"`
	RetryMax     int      `yaml:"retry_max"`
	RetryBackoff int      `yaml:"retry_backoff_ms"`
	Log          bool     `yaml:"log"`
	LogLevel     string   `yaml:"log_level"`
	LogMsg       string   `yaml:"log_msg"`
}

type yamlStream struct {
	Keywords []string `yaml:"keywords"`
}

// ---------------------------------------------------------------------------
// Compiled config (precompiled templates, atomic swap)
// ---------------------------------------------------------------------------

type compiledConfig struct {
	enabled       bool
	rules         []compiledRule
	streamKeywords []string
	modelMatching  string // "exact" or "wildcard"
}

type compiledRule struct {
	name         string
	paths        []string
	streamPaths  []string
	operator     string
	values       []int64
	models       []string
	action       string
	errorStatus  int
	errorBody    *template.Template
	streamError  *template.Template
	retryMax     int
	retryBackoff int
	log          bool
	logLevel     string
	logMsg       *template.Template
}

// Template context for error bodies and log messages.
type tplCtx struct {
	Rule    string
	Model   string
	Tokens  int64
	Path    string
	Attempt int
}

var configPtr atomic.Pointer[compiledConfig]

func getConfig() *compiledConfig {
	return configPtr.Load()
}

// ---------------------------------------------------------------------------
// Default values
// ---------------------------------------------------------------------------

var builtinDefaults = yamlDefaults{
	Paths: []string{
		"usage.reasoning_tokens",
		"usage.completion_tokens_details.reasoning_tokens",
		"usage.output_tokens_details.reasoning_tokens",
		"reasoning_tokens",
	},
	StreamPaths: []string{
		"response.usage.output_tokens_details.reasoning_tokens",
		"response.usage.reasoning_tokens",
	},
	Operator:    "exact",
	Values:      []int64{516},
	Models:      nil,
	Action:      "error",
	ErrorStatus: 503,
	ErrorBody:   `{"error":{"message":"Reasoning degradation detected (tokens={{.Tokens}}).","type":"degradation_filter","code":"reasoning_516","param":null}}`,
	StreamError: "data: {\"error\":{\"message\":\"Reasoning degradation detected.\",\"type\":\"degradation_filter\",\"code\":\"reasoning_516\"}}\n\ndata: [DONE]\n\n",
	RetryMax:    1,
	Log:         true,
	LogLevel:    "warn",
	LogMsg:      "respfilter[{{.Rule}}]: degradation model={{.Model}} tokens={{.Tokens}} path={{.Path}}",
}

// ---------------------------------------------------------------------------
// Config parsing and compilation
// ---------------------------------------------------------------------------

// parseConfig decodes the config_yaml from the plugin.register/reconfigure
// lifecycle request and compiles a new compiledConfig. Templates are
// precompiled for efficiency. The result is atomically stored.
func parseConfig(configYAML []byte) error {
	cfg := compiledConfig{
		enabled:        true,
		modelMatching:  "wildcard",
		streamKeywords: []string{"usage", "reasoning"},
	}

	yc := yamlConfig{
		Defaults:      builtinDefaults,
		ModelMatching: "wildcard",
	}

	if len(configYAML) > 0 {
		if err := yaml.Unmarshal(configYAML, &yc); err != nil {
			return err
		}
	}

	cfg.enabled = yc.Enabled
	if yc.ModelMatching != "" {
		cfg.modelMatching = yc.ModelMatching
	}

	if len(yc.Stream.Keywords) > 0 {
		cfg.streamKeywords = yc.Stream.Keywords
	}

	defaults := mergeDefaults(yc.Defaults)

	if len(yc.Rules) == 0 {
		// No explicit rules — create one rule from defaults.
		yc.Rules = []yamlRule{{Name: "default"}}
	}

	rules := make([]compiledRule, 0, len(yc.Rules))
	for _, yr := range yc.Rules {
		cr, err := compileRule(yr, defaults)
		if err != nil {
			return err
		}
		rules = append(rules, cr)
	}
	cfg.rules = rules

	configPtr.Store(&cfg)
	return nil
}

func mergeDefaults(userDefaults yamlDefaults) yamlDefaults {
	d := builtinDefaults
	ud := userDefaults

	if len(ud.Paths) > 0 {
		d.Paths = ud.Paths
	}
	if len(ud.StreamPaths) > 0 {
		d.StreamPaths = ud.StreamPaths
	}
	if ud.Operator != "" {
		d.Operator = ud.Operator
	}
	if len(ud.Values) > 0 {
		d.Values = ud.Values
	}
	if ud.Models != nil {
		d.Models = ud.Models
	}
	if ud.Action != "" {
		d.Action = ud.Action
	}
	if ud.ErrorStatus > 0 {
		d.ErrorStatus = ud.ErrorStatus
	}
	if ud.ErrorBody != "" {
		d.ErrorBody = ud.ErrorBody
	}
	if ud.StreamError != "" {
		d.StreamError = ud.StreamError
	}
	if ud.RetryMax > 0 {
		d.RetryMax = ud.RetryMax
	}
	if ud.RetryBackoff > 0 {
		d.RetryBackoff = ud.RetryBackoff
	}
	d.Log = ud.Log
	if ud.LogLevel != "" {
		d.LogLevel = ud.LogLevel
	}
	if ud.LogMsg != "" {
		d.LogMsg = ud.LogMsg
	}
	return d
}

func compileRule(yr yamlRule, d yamlDefaults) (compiledRule, error) {
	cr := compiledRule{
		name:         strOrDefault(yr.Name, "unnamed"),
		paths:        strSliceOr(yr.Paths, d.Paths),
		streamPaths:  strSliceOr(yr.StreamPaths, d.StreamPaths),
		operator:     strOrDefault(yr.Operator, d.Operator),
		values:       intSliceOr(yr.Values, d.Values),
		models:       yr.Models,
		action:       strOrDefault(yr.Action, d.Action),
		errorStatus:  intOr(yr.ErrorStatus, d.ErrorStatus),
		retryMax:     intOr(yr.RetryMax, d.RetryMax),
		retryBackoff: intOr(yr.RetryBackoff, d.RetryBackoff),
		logLevel:     strOrDefault(yr.LogLevel, d.LogLevel),
	}

	if yr.Log != nil {
		cr.log = *yr.Log
	} else {
		cr.log = d.Log
	}

	errorBody := strOrDefault(yr.ErrorBody, d.ErrorBody)
	tpl, err := template.New("error_body").Parse(errorBody)
	if err != nil {
		return compiledRule{}, err
	}
	cr.errorBody = tpl

	streamError := strOrDefault(yr.StreamError, d.StreamError)
	stpl, err := template.New("stream_error").Parse(streamError)
	if err != nil {
		return compiledRule{}, err
	}
	cr.streamError = stpl

	logMsg := strOrDefault(yr.LogMsg, d.LogMsg)
	ltpl, err := template.New("log_msg").Parse(logMsg)
	if err != nil {
		return compiledRule{}, err
	}
	cr.logMsg = ltpl

	return cr, nil
}

// ---------------------------------------------------------------------------
// Lifecycle request (shared by register and reconfigure)
// ---------------------------------------------------------------------------

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strOrDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

func strSliceOr(s, d []string) []string {
	if len(s) > 0 {
		return s
	}
	return d
}

func intSliceOr(v, d []int64) []int64 {
	if len(v) > 0 {
		return v
	}
	return d
}

func intOr(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// ---------------------------------------------------------------------------
// Registration response types
// ---------------------------------------------------------------------------

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      registrationMetadata   `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationMetadata struct {
	Name             string              `json:"Name"`
	Version          string              `json:"Version"`
	Author           string              `json:"Author"`
	GitHubRepository string              `json:"GitHubRepository"`
	Logo             string              `json:"Logo"`
	ConfigFields     []registrationField `json:"ConfigFields"`
}

type registrationField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type registrationCapability struct {
	ResponseAfterTranslator bool `json:"response_after_translator"`
	StreamChunkInterceptor  bool `json:"response_stream_interceptor"`
	UsagePlugin             bool `json:"usage_plugin"`
	ManagementAPI           bool `json:"management_api"`
}

func buildRegistration() registration {
	return registration{
		SchemaVersion: 1,
		Metadata: registrationMetadata{
			Name:             "respfilter",
			Version:          "2.0.0",
			Author:           "cpa-respfilter",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "",
			ConfigFields: []registrationField{
				{Name: "rules", Type: "array", Description: "Detection rules. Each rule specifies paths, operator, values, model filter, and action. First match wins."},
				{Name: "defaults", Type: "object", Description: "Default values for rules that omit a field."},
				{Name: "stream", Type: "object", Description: "Stream optimization: keywords to pre-filter chunks before JSON parsing."},
				{Name: "model_matching", Type: "enum", EnumValues: []string{"exact", "wildcard"}, Description: "Model name matching mode for rule model filters."},
			},
		},
		Capabilities: registrationCapability{
			ResponseAfterTranslator: true,
			StreamChunkInterceptor:  true,
			UsagePlugin:             true,
			ManagementAPI:           true,
		},
	}
}

// ---------------------------------------------------------------------------
// Rule stats (persist across config reloads, keyed by rule name)
// ---------------------------------------------------------------------------

type ruleStats struct {
	matches        atomic.Int64
	retries        atomic.Int64
	retrySuccesses atomic.Int64
}

var ruleStatsMap sync.Map

func getRuleStats(name string) *ruleStats {
	if s, ok := ruleStatsMap.Load(name); ok {
		return s.(*ruleStats)
	}
	s := &ruleStats{}
	actual, _ := ruleStatsMap.LoadOrStore(name, s)
	return actual.(*ruleStats)
}

func allRuleStats() map[string]ruleStatsSnapshot {
	result := make(map[string]ruleStatsSnapshot)
	ruleStatsMap.Range(func(key, val any) bool {
		result[key.(string)] = val.(*ruleStats).snapshot()
		return true
	})
	return result
}

type ruleStatsSnapshot struct {
	Matches        int64 `json:"matches"`
	Retries        int64 `json:"retries"`
	RetrySuccesses int64 `json:"retry_successes"`
}

func (s *ruleStats) snapshot() ruleStatsSnapshot {
	return ruleStatsSnapshot{
		Matches:        s.matches.Load(),
		Retries:        s.retries.Load(),
		RetrySuccesses: s.retrySuccesses.Load(),
	}
}

// ---------------------------------------------------------------------------
// Envelope (shared by all handlers)
// ---------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func okEnvelopeRaw(result string) []byte {
	raw, _ := json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
	return raw
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// Keep strings import alive (used by detect.go via wildcardMatch).
var _ = strings.Contains
