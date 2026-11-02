package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compare the documented grammar exception with full request normalization.
// The required case intentionally probes a conflict with the current policy,
// which converts required to auto. No inference backend is involved.
func TestShortOutputGrammarSurvivesGatewayPipeline(t *testing.T) {
	const model = "test-model"
	cases := []struct {
		name       string
		choice     any
		jsonObject bool
		grammar    bool
	}{
		{name: "required", choice: "required", grammar: true},
		{name: "named", choice: map[string]any{"type": "function", "function": map[string]any{"name": "answer"}}, grammar: true},
		{name: "json_object", choice: "auto", jsonObject: true, grammar: true},
		{name: "auto_control", choice: "auto"},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				request := map[string]any{
					"model": model, "stream": stream, "max_tokens": 100, "min_tokens": 64,
					"messages":    []any{map[string]any{"role": "user", "content": "Call answer with value OK."}},
					"tool_choice": tc.choice,
					"tools": []any{map[string]any{"type": "function", "function": map[string]any{
						"name": "answer", "parameters": map[string]any{
							"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{"OK"}}},
							"required": []string{"value"}, "additionalProperties": false,
						},
					}}},
				}
				if tc.jsonObject {
					request["response_format"] = map[string]any{"type": "json_object"}
				}
				body, err := json.Marshal(request)
				require.NoError(t, err)
				var direct map[string]any
				require.NoError(t, json.Unmarshal(body, &direct))
				require.Equal(t, tc.grammar, completionapi.GrammarConstrained(direct))
				completionapi.EnforceTokenBudget(direct)

				normalized, declared, err := normalizeChatRequestForModel(body, model)
				require.NoError(t, err)
				var forwarded map[string]any
				require.NoError(t, json.Unmarshal(normalized, &forwarded))
				assert.Equal(t, tc.choice, forwarded["tool_choice"], "normalized tool choice differs from the requested constraint")
				assert.Equal(t, tc.grammar, completionapi.GrammarConstrained(forwarded), "grammar classification changed before execution")
				assert.EqualValues(t, 100, declared.MaxTokens)
				assert.EqualValues(t, 100, forwarded["max_tokens"])
				if tc.grammar {
					require.NotContains(t, direct, "min_tokens")
					assert.NotContains(t, forwarded, "min_tokens", "documented grammar exception removes the caller minimum")
				} else {
					assert.EqualValues(t, 64, forwarded["min_tokens"], "auto is a non-grammar control and keeps the caller minimum")
				}
				// Reapplying the token budget should preserve the grammar exception.
				completionapi.EnforceTokenBudget(forwarded)
				if tc.grammar {
					assert.NotContains(t, forwarded, "min_tokens", "downstream normalization must retain the grammar exception")
				}
			})
		}
	}
}
