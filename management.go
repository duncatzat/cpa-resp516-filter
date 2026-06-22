package main

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Wire types for management.register and management.handle
// ---------------------------------------------------------------------------

// management.register request (pluginapi.ManagementRegistrationRequest — no json tags)
type managementRegisterRequest struct {
	Plugin           registrationMetadata `json:"Plugin"`
	BasePath         string               `json:"BasePath"`
	ResourceBasePath string               `json:"ResourceBasePath"`
}

// management.register response — routes and resources use snake_case json tags
// (matching rpcManagementRegistrationResponse in the host).
type managementRegisterResponse struct {
	Routes    []managementRouteDecl `json:"routes,omitempty"`
	Resources []managementRouteDecl `json:"resources,omitempty"`
}

type managementRouteDecl struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

// management.handle request (pluginapi.ManagementRequest — no json tags)
type managementHandleRequest struct {
	Method  string              `json:"Method"`
	Path    string              `json:"Path"`
	Headers map[string][]string `json:"Headers"`
	Query   map[string][]string `json:"Query"`
	Body    []byte              `json:"Body"`
}

// management.handle response (pluginapi.ManagementResponse — no json tags)
type managementHandleResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

// ---------------------------------------------------------------------------
// Handler: management.register
// ---------------------------------------------------------------------------

func handleManagementRegister(raw []byte) ([]byte, error) {
	// Parse the request to extract the resource base path (useful for logging).
	var req managementRegisterRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	resp := managementRegisterResponse{
		Resources: []managementRouteDecl{
			{
				Path:        "/status",
				Menu:        "RespFilter Status",
				Description: "View current detection rules, configuration, and runtime statistics.",
			},
		},
	}
	return okEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Handler: management.handle — serves /status
// ---------------------------------------------------------------------------

func handleManagement(raw []byte) ([]byte, error) {
	var req managementHandleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	status := buildStatusJSON()
	body, _ := json.MarshalIndent(status, "", "  ")

	resp := managementHandleResponse{
		StatusCode: 200,
		Headers: map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		Body: body,
	}
	return okEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Status response builder
// ---------------------------------------------------------------------------

func buildStatusJSON() map[string]any {
	cfg := getConfig()

	result := map[string]any{
		"plugin":    "respfilter",
		"version":   "2.0.0",
		"enabled":   cfg != nil && cfg.enabled,
		"stats":     allRuleStats(),
	}

	if cfg != nil {
		rules := make([]map[string]any, 0, len(cfg.rules))
		for _, r := range cfg.rules {
			rule := map[string]any{
				"name":          r.name,
				"operator":      r.operator,
				"values":        r.values,
				"action":        r.action,
				"models":        r.models,
				"paths":         r.paths,
				"stream_paths":  r.streamPaths,
				"retry_max":     r.retryMax,
				"retry_backoff": r.retryBackoff,
				"log":           r.log,
			}
			rules = append(rules, rule)
		}
		result["rules"] = rules
		result["model_matching"] = cfg.modelMatching
		result["stream_keywords"] = cfg.streamKeywords
	}

	return result
}
