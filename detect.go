package main

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// matchOperator returns true if the detected token value matches the rule's
// operator and values.
func matchOperator(operator string, values []int64, tokens int64) bool {
	switch operator {
	case "exact":
		return len(values) > 0 && tokens == values[0]
	case "any_of":
		for _, v := range values {
			if tokens == v {
				return true
			}
		}
		return false
	case "lte":
		return len(values) > 0 && tokens <= values[0]
	case "gte":
		return len(values) > 0 && tokens >= values[0]
	case "range":
		if len(values) < 2 {
			return false
		}
		return tokens >= values[0] && tokens <= values[1]
	case "not_in":
		for _, v := range values {
			if tokens == v {
				return false
			}
		}
		return true
	case "always":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Model matching
// ---------------------------------------------------------------------------

// modelMatches returns true if the model matches any pattern in the rule's
// model filter. Empty filter matches all models. Supports wildcard matching
// when modelMatching is "wildcard" (default), exact otherwise.
func modelMatches(patterns []string, model, mode string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if mode == "wildcard" {
			if wildcardMatch(p, model) {
				return true
			}
		} else {
			if strings.EqualFold(p, model) {
				return true
			}
		}
	}
	return false
}

// wildcardMatch matches a pattern with * wildcards (prefix, suffix, infix)
// against a string, case-insensitively.
func wildcardMatch(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, s)
	}
	lp := strings.ToLower(pattern)
	ls := strings.ToLower(s)
	if strings.HasPrefix(lp, "*") && strings.HasSuffix(lp, "*") {
		infix := strings.Trim(lp, "*")
		return strings.Contains(ls, infix)
	}
	if strings.HasPrefix(lp, "*") {
		suffix := strings.TrimPrefix(lp, "*")
		return strings.HasSuffix(ls, suffix)
	}
	if strings.HasSuffix(lp, "*") {
		prefix := strings.TrimSuffix(lp, "*")
		return strings.HasPrefix(ls, prefix)
	}
	return strings.EqualFold(lp, ls)
}

// ---------------------------------------------------------------------------
// Path extraction — non-streaming responses
// ---------------------------------------------------------------------------

// extractReasoningTokens checks all configured paths in order. Returns the
// token value and the path where it was found, or (-1, "") if no path exists.
func extractReasoningTokens(body []byte, paths []string) (int64, string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return -1, ""
	}
	for _, p := range paths {
		if v := gjson.GetBytes(body, p); v.Exists() {
			return v.Int(), p
		}
	}
	return -1, ""
}

// ---------------------------------------------------------------------------
// Path extraction — streaming SSE chunks
// ---------------------------------------------------------------------------

// extractReasoningTokensFromChunk parses SSE-formatted chunk data. Each chunk
// may contain multiple "data:" lines. For each JSON payload, it checks both
// the non-stream paths and the stream-specific paths (which handle Responses
// API nesting like response.usage.*).
//
// Returns the maximum reasoning token value found and its path, or (-1, "")
// if no usage data is present.
func extractReasoningTokensFromChunk(chunk []byte, nonStreamPaths, streamPaths []string) (int64, string) {
	if len(chunk) == 0 {
		return -1, ""
	}
	text := string(chunk)

	// Fast path: if the chunk is not SSE-formatted, try direct JSON parse.
	if !strings.Contains(text, "data:") {
		if gjson.Valid(text) {
			return extractReasoningTokens(chunk, mergePaths(nonStreamPaths, streamPaths))
		}
		return -1, ""
	}

	var maxTokens int64 = -1
	var foundPath string

	allPaths := mergePaths(nonStreamPaths, streamPaths)

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" || payload == "" {
			continue
		}
		if !gjson.Valid(payload) {
			continue
		}
		for _, p := range allPaths {
			if v := gjson.Get(payload, p); v.Exists() {
				t := v.Int()
				if t > maxTokens {
					maxTokens = t
					foundPath = p
				}
				break
			}
		}
	}
	return maxTokens, foundPath
}

// ---------------------------------------------------------------------------
// Stream keyword pre-filter
// ---------------------------------------------------------------------------

// chunkContainsKeyword returns true if the chunk data contains any of the
// configured keywords (case-insensitive substring match). This is a fast
// pre-filter to avoid JSON parsing on every SSE chunk.
func chunkContainsKeyword(chunk []byte, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	text := strings.ToLower(string(chunk))
	for _, kw := range keywords {
		if strings.Contains(text, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mergePaths(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, p := range a {
		if !seen[p] {
			merged = append(merged, p)
			seen[p] = true
		}
	}
	for _, p := range b {
		if !seen[p] {
			merged = append(merged, p)
			seen[p] = true
		}
	}
	return merged
}
