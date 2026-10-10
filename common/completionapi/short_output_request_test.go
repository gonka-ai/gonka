package completionapi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/require"
)

// Only a caller min_tokens survives (clamped to max), short max_tokens stay short, and
// stop_token_ids are kept for the vocab check instead of being dropped.
func TestEnforceTokenBudget(t *testing.T) {
	stopIDs := []interface{}{float64(7), float64(9)}
	tests := []struct {
		name        string
		requestMap  map[string]interface{}
		expectedMin interface{}
		expectedMax int
	}{
		{"AbsentMinStaysAbsent", map[string]interface{}{"max_tokens": float64(100)}, nil, 100},
		{"ShortMaxNotRaised", map[string]interface{}{"max_tokens": float64(8)}, nil, 8},
		{"CallerMinKept", map[string]interface{}{"min_tokens": float64(16), "max_tokens": float64(100)}, 16, 100},
		{"CallerMinClampedToMax", map[string]interface{}{"min_tokens": float64(16), "max_tokens": float64(8)}, 8, 8},
		{"ZeroMinDropped", map[string]interface{}{"min_tokens": float64(0), "max_tokens": float64(100)}, nil, 100},
		{"NegativeMinDropped", map[string]interface{}{"min_tokens": float64(-5), "max_tokens": float64(100)}, nil, 100},
		{"UnusableMinTypeDropped", map[string]interface{}{"min_tokens": "oops", "max_tokens": float64(100)}, nil, 100},
		{"MaxCompletionTokensOnly", map[string]interface{}{"max_completion_tokens": float64(4)}, nil, 4},
		{"AbsentMaxUsesDefault", map[string]interface{}{}, nil, calculations.DefaultMaxTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.requestMap["stop_token_ids"] = stopIDs
			EnforceTokenBudget(tt.requestMap)
			if tt.expectedMin == nil {
				require.NotContains(t, tt.requestMap, "min_tokens")
			} else {
				require.Equal(t, tt.expectedMin, tt.requestMap["min_tokens"])
			}
			require.Equal(t, tt.expectedMax, tt.requestMap["max_tokens"])
			require.Equal(t, tt.expectedMax, tt.requestMap["max_completion_tokens"])
			require.Equal(t, stopIDs, tt.requestMap["stop_token_ids"], "never silently stripped")
		})
	}
}

// The rewrite asks the engine for no prompt logprobs: validation scores output positions only.
func TestModifyRequestBodyAsksNoPromptLogprobs(t *testing.T) {
	for name, body := range map[string]string{
		"not streamed": `{"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		"streamed":     `{"max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			modified, err := ModifyRequestBodyWithLogprobsMode([]byte(body), 7, "processed")
			require.NoError(t, err)
			var raw map[string]interface{}
			require.NoError(t, json.Unmarshal(modified.NewBody, &raw))
			require.NotContains(t, raw, "prompt_logprobs")
		})
	}
}

func TestValidateStopTokenIDs(t *testing.T) {
	const vocabularySize = 1000
	tests := []struct {
		name           string
		stopTokenIDs   interface{}
		vocabularySize int
		wantErr        bool
	}{
		{"absent", nil, vocabularySize, false},
		{"empty", []interface{}{}, 0, false},
		{"in range", []interface{}{float64(0), float64(999)}, vocabularySize, false},
		{"json numbers", []interface{}{json.Number("5")}, vocabularySize, false},
		{"at vocab size", []interface{}{float64(1000)}, vocabularySize, true},
		{"far out of range", []interface{}{float64(7), float64(9999999)}, vocabularySize, true},
		{"negative", []interface{}{float64(-1)}, vocabularySize, true},
		{"fractional", []interface{}{float64(1.5)}, vocabularySize, true},
		{"string", []interface{}{"7"}, vocabularySize, true},
		{"not an array", float64(7), vocabularySize, true},
		{"unknown vocab", []interface{}{float64(7)}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestMap := map[string]interface{}{}
			if tt.stopTokenIDs != nil {
				requestMap["stop_token_ids"] = tt.stopTokenIDs
			}
			err := ValidateStopTokenIDs(requestMap, tt.vocabularySize)
			if tt.wantErr {
				require.True(t, errors.Is(err, ErrStopTokenIDs), "got %v", err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStopTokenIDsOf(t *testing.T) {
	ids := StopTokenIDsOf(map[string]interface{}{"stop_token_ids": []interface{}{float64(7), json.Number("200020"), "x"}})
	require.Equal(t, map[string]struct{}{"7": {}, "200020": {}}, ids)
	require.Nil(t, StopTokenIDsOf(map[string]interface{}{}))
}
