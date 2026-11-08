package completionapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Enforce the token budget on grammar-constrained requests that carry a caller min_tokens.
// 2. min_tokens is dropped and max_tokens/max_completion_tokens keep the caller's value.
func TestEnforceTokenBudget_GrammarConstrainedDropsMinTokens(t *testing.T) {
	constraints := map[string]map[string]interface{}{
		"response_format json_schema":    {"response_format": map[string]interface{}{"type": "json_schema", "json_schema": map[string]interface{}{"name": "a", "schema": map[string]interface{}{"type": "object"}}}},
		"response_format json_object":    {"response_format": map[string]interface{}{"type": "json_object"}},
		"response_format structural_tag": {"response_format": map[string]interface{}{"type": "structural_tag"}},
		"structured_outputs json":        {"structured_outputs": map[string]interface{}{"json": map[string]interface{}{"type": "object"}}},
		"structured_outputs regex":       {"structured_outputs": map[string]interface{}{"regex": "[a-z]+"}},
		"structured_outputs empty regex": {"structured_outputs": map[string]interface{}{"regex": ""}},
		"structured_outputs choice":      {"structured_outputs": map[string]interface{}{"choice": []interface{}{"yes", "no"}}},
		"structured_outputs grammar":     {"structured_outputs": map[string]interface{}{"grammar": `root ::= "a"`}},
		"structured_outputs json_object": {"structured_outputs": map[string]interface{}{"json_object": true}},
		"structured_outputs tag":         {"structured_outputs": map[string]interface{}{"structural_tag": map[string]interface{}{"type": "structural_tag"}}},
		"tool_choice required":           {"tool_choice": "required"},
		"tool_choice named":              {"tool_choice": map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "f"}}},
	}
	for name, constraint := range constraints {
		t.Run(name, func(t *testing.T) {
			requestMap := map[string]interface{}{"min_tokens": float64(16), "max_tokens": float64(100)}
			for key, value := range constraint {
				requestMap[key] = value
			}
			EnforceTokenBudget(requestMap)
			require.NotContains(t, requestMap, "min_tokens")
			require.Equal(t, 100, requestMap["max_tokens"])
			require.Equal(t, 100, requestMap["max_completion_tokens"])
		})
	}
}

// Test flow:
// 1. Call GrammarConstrained on requests whose fields do not make vLLM apply a grammar.
// 2. Each reports false, and EnforceTokenBudget keeps the caller min_tokens on them.
func TestGrammarConstrained_UnconstrainedRequests(t *testing.T) {
	requests := map[string]map[string]interface{}{
		"plain":                             {},
		"response_format text":              {"response_format": map[string]interface{}{"type": "text"}},
		"response_format not an object":     {"response_format": "json_object"},
		"structured_outputs options only":   {"structured_outputs": map[string]interface{}{"disable_any_whitespace": true}},
		"structured_outputs json_object no": {"structured_outputs": map[string]interface{}{"json_object": false}},
		"structured_outputs null regex":     {"structured_outputs": map[string]interface{}{"regex": nil}},
		"guided_json":                       {"guided_json": map[string]interface{}{"type": "object"}},
		"tool_choice auto":                  {"tool_choice": "auto"},
		"tool_choice none":                  {"tool_choice": "none"},
		"tool_choice object without name":   {"tool_choice": map[string]interface{}{"type": "function"}},
		"tool_choice object empty name":     {"tool_choice": map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": ""}}},
	}
	for name, requestMap := range requests {
		t.Run(name, func(t *testing.T) {
			require.False(t, GrammarConstrained(requestMap))
			requestMap["min_tokens"] = float64(16)
			requestMap["max_tokens"] = float64(100)
			EnforceTokenBudget(requestMap)
			require.Equal(t, 16, requestMap["min_tokens"])
		})
	}
}
