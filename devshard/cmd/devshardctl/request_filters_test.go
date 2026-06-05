package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"devshard/cmd/devshardctl/paramvalidators"

	"github.com/stretchr/testify/require"
)

func TestNormalizeChatRequestDefaultsAndCapsOutputTokens(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 3_072
	RequestMaxTokensCap = 4_096
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
	})

	body, req, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 3_072, req.MaxTokens)
	require.Zero(t, req.MaxCompletionTokens)
	require.Contains(t, string(body), `"max_tokens":3072`)
	require.NotContains(t, string(body), `"max_completion_tokens"`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 64, req.MaxTokens)
	require.Zero(t, req.MaxCompletionTokens)
	require.Contains(t, string(body), `"max_tokens":64`)
	require.NotContains(t, string(body), `"max_completion_tokens"`)

	body, req, err = normalizeChatRequest([]byte(`{"max_completion_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 64, req.MaxTokens)
	require.EqualValues(t, 64, req.MaxCompletionTokens)
	require.NotContains(t, string(body), `"max_tokens"`)
	require.Contains(t, string(body), `"max_completion_tokens":64`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":10001,"max_completion_tokens":20000,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 4_096, req.MaxTokens)
	require.EqualValues(t, 4_096, req.MaxCompletionTokens)
	require.Contains(t, string(body), `"max_tokens":4096`)
	require.Contains(t, string(body), `"max_completion_tokens":4096`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":64,"max_completion_tokens":10000,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 64, req.MaxTokens)
	require.EqualValues(t, 64, req.MaxCompletionTokens)
	require.Contains(t, string(body), `"max_tokens":64`)
	require.Contains(t, string(body), `"max_completion_tokens":64`)
}

func TestNormalizeChatRequestUsesProvidedOutputTokenLimits(t *testing.T) {
	limits := outputTokenLimits{DefaultMaxTokens: 2_048, MaxTokensCap: 3_584}

	body, req, err := normalizeChatRequestForAuthAndLimits([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), false, limits, "")
	require.NoError(t, err)
	require.EqualValues(t, 2_048, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":2048`)

	body, req, err = normalizeChatRequestForAuthAndLimits([]byte(`{"max_tokens":4096,"messages":[{"role":"user","content":"hello"}]}`), false, limits, "")
	require.NoError(t, err)
	require.EqualValues(t, 3_584, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":3584`)
}

func TestPrepareChatRequestBodyAdminAuthBypassesOutputTokenCap(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 3_072
	RequestMaxTokensCap = 4_096
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"max_tokens": 20000,
		"max_completion_tokens": 30000,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	req = req.WithContext(context.WithValue(req.Context(), adminAuthContextKey{}, true))

	body, chatReq, err := prepareChatRequestBody(req)
	require.NoError(t, err)
	require.EqualValues(t, 20_000, chatReq.MaxTokens)
	require.EqualValues(t, 20_000, chatReq.MaxCompletionTokens)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 20_000, raw["max_tokens"])
	require.EqualValues(t, 20_000, raw["max_completion_tokens"])
}

func TestPrepareChatRequestBodyAdminAuthKeepsMaxCompletionTokensAboveDefault(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 3_072
	RequestMaxTokensCap = 4_096
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"max_completion_tokens": 30000,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	req = req.WithContext(context.WithValue(req.Context(), adminAuthContextKey{}, true))

	body, chatReq, err := prepareChatRequestBody(req)
	require.NoError(t, err)
	require.EqualValues(t, 30_000, chatReq.MaxTokens)
	require.EqualValues(t, 30_000, chatReq.MaxCompletionTokens)
	require.NotContains(t, string(body), `"max_tokens"`)
	require.Contains(t, string(body), `"max_completion_tokens":30000`)
}

func TestPrepareChatRequestBodyPreservesLargeIntegerFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"seed": 9007199254740993,
		"messages": [{"role": "user", "content": "hello"}]
	}`))

	body, _, err := prepareChatRequestBody(req)
	require.NoError(t, err)

	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&raw))
	seed, ok := raw["seed"].(json.Number)
	require.True(t, ok)
	require.Equal(t, "9007199254740993", seed.String())
	require.Contains(t, string(body), `"seed":9007199254740993`)
}

func TestNormalizeChatRequestForcesSingleChoiceWithGreedySampling(t *testing.T) {
	// vLLM rejects `n > 1` when `temperature == 0` (greedy sampling produces identical
	// completions). Coerce silently to n=1 so 3000+ wasted retries don't reach the engine.
	coerceCases := []string{
		`{"n":2,"temperature":0,"messages":[{"role":"user","content":"hi"}]}`,
		`{"n":5,"temperature":0,"messages":[{"role":"user","content":"hi"}]}`,
		`{"n":5,"temperature":0.0,"messages":[{"role":"user","content":"hi"}]}`,
	}
	for _, body := range coerceCases {
		t.Run("coerce_"+body, func(t *testing.T) {
			out, req, err := normalizeChatRequest([]byte(body))
			require.NoError(t, err)
			require.EqualValues(t, 1, req.N)
			require.Contains(t, string(out), `"n":1`)
		})
	}

	passThroughCases := []struct {
		body    string
		wantN   uint64
		wantStr string
	}{
		{body: `{"n":1,"temperature":0,"messages":[{"role":"user","content":"hi"}]}`, wantN: 1, wantStr: `"n":1`},
		{body: `{"n":5,"temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`, wantN: 5, wantStr: `"n":5`},
		{body: `{"n":5,"messages":[{"role":"user","content":"hi"}]}`, wantN: 5, wantStr: `"n":5`},
		{body: `{"n":5,"temperature":0.0001,"messages":[{"role":"user","content":"hi"}]}`, wantN: 5, wantStr: `"n":5`},
	}
	for _, tc := range passThroughCases {
		t.Run("keep_"+tc.body, func(t *testing.T) {
			out, req, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			require.EqualValues(t, tc.wantN, req.N)
			require.Contains(t, string(out), tc.wantStr)
		})
	}
}

func TestNormalizeChatRequestCapsChoices(t *testing.T) {
	body, req, err := normalizeChatRequest([]byte(`{"n":1638400,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, MaxChatRequestChoices, req.N)
	require.Contains(t, string(body), `"n":5`)

	body, req, err = normalizeChatRequest([]byte(`{"n":3,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 3, req.N)
	require.Contains(t, string(body), `"n":3`)

	body, req, err = normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.Zero(t, req.N)
	require.NotContains(t, string(body), `"n"`)
}

func TestNormalizeChatRequestClampsMinTokensAboveEffectiveMax(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 1_000
	RequestMaxTokensCap = 2_000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
	})

	body, req, err := normalizeChatRequest([]byte(`{
		"max_tokens": 9999,
		"min_tokens": 9994,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)
	require.EqualValues(t, 2_000, req.MaxTokens)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 2_000, raw["max_tokens"])
	require.EqualValues(t, 2_000, raw["min_tokens"])
}

func TestNormalizeChatRequestKeepsMinTokensWithinEffectiveMax(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 1_000
	RequestMaxTokensCap = 2_000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
	})

	body, req, err := normalizeChatRequest([]byte(`{
		"max_tokens": 9999,
		"min_tokens": 128,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)
	require.EqualValues(t, 2_000, req.MaxTokens)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 2_000, raw["max_tokens"])
	require.EqualValues(t, 128, raw["min_tokens"])
}

func TestNormalizeChatRequestStripsTemperatureAboveMax(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 999999
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 2.0, raw["temperature"])
}

func TestNormalizeChatRequestKeepsTemperatureWithinMax(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 1.5
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 1.5, raw["temperature"])
}

func TestNormalizeChatRequestClampsFrequencyAndPresencePenalty(t *testing.T) {
	// OpenAI/vLLM accept [-2.0, 2.0] for both. Catalog clamps; out-of-range is rewritten,
	// not rejected. Per-Kimi force-zero is exercised separately under ApplyKimiRequestOverrides.
	tests := []struct {
		name  string
		body  string
		field string
		want  float64
	}{
		{name: "freq above max", body: `{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":5}`, field: "frequency_penalty", want: 2.0},
		{name: "freq below min", body: `{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":-5}`, field: "frequency_penalty", want: -2.0},
		{name: "freq within range", body: `{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":0.5}`, field: "frequency_penalty", want: 0.5},
		{name: "pres above max", body: `{"messages":[{"role":"user","content":"hi"}],"presence_penalty":3.5}`, field: "presence_penalty", want: 2.0},
		{name: "pres below min", body: `{"messages":[{"role":"user","content":"hi"}],"presence_penalty":-3.5}`, field: "presence_penalty", want: -2.0},
		{name: "pres zero", body: `{"messages":[{"role":"user","content":"hi"}],"presence_penalty":0}`, field: "presence_penalty", want: 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := normalizeChatRequest([]byte(tt.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			require.EqualValues(t, tt.want, raw[tt.field])
		})
	}
}

func TestNormalizeChatRequestStripsNonFiniteFrequencyAndPresencePenalty(t *testing.T) {
	tests := []string{
		`{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":"infinity"}`,
		`{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":"nan"}`,
		`{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":"not-a-number"}`,
		`{"messages":[{"role":"user","content":"hi"}],"presence_penalty":"infinity"}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "frequency_penalty")
			require.NotContains(t, raw, "presence_penalty")
		})
	}
}

func TestNormalizeForKimiForceZerosPenalties(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "freq nonzero", body: `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"frequency_penalty":0.5}`},
		{name: "pres nonzero", body: `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"presence_penalty":-1.5}`},
		{name: "both nonzero", body: `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"frequency_penalty":2,"presence_penalty":-2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := normalizeChatRequestForModel([]byte(tt.body), kimiK26ModelID)
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			if _, has := raw["frequency_penalty"]; has {
				require.EqualValues(t, 0.0, raw["frequency_penalty"])
			}
			if _, has := raw["presence_penalty"]; has {
				require.EqualValues(t, 0.0, raw["presence_penalty"])
			}
		})
	}
}

func TestNormalizeForKimiLeavesPenaltiesAlreadyZero(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"frequency_penalty":0,"presence_penalty":0}`
	out, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.EqualValues(t, 0.0, raw["frequency_penalty"])
	require.EqualValues(t, 0.0, raw["presence_penalty"])
}

func TestNormalizeDoesNotForceZeroPenaltiesForOtherModels(t *testing.T) {
	body := `{"model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","messages":[{"role":"user","content":"hi"}],"frequency_penalty":0.5,"presence_penalty":-0.5}`
	out, _, err := normalizeChatRequestForModel([]byte(body), "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8")
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.EqualValues(t, 0.5, raw["frequency_penalty"])
	require.EqualValues(t, -0.5, raw["presence_penalty"])
}

func TestNormalizeForKimiDoesNotAddPenaltiesWhenAbsent(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}]}`
	out, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "frequency_penalty")
	require.NotContains(t, raw, "presence_penalty")
}

func TestNormalizeChatRequestForcesValidationLogprobs(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"logprobs": false,
		"top_logprobs": 20
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, true, raw["logprobs"])
	require.EqualValues(t, 5, raw["top_logprobs"])
}

func TestNormalizeChatRequestForcesLogprobsTrue(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"logprobs": false
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, true, raw["logprobs"])
}

func TestNormalizeChatRequestForcesTopLogprobsFive(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"top_logprobs": 1
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 5, raw["top_logprobs"])
}

func TestNormalizeChatRequestRejectsPromptLogprobs(t *testing.T) {
	_, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"prompt_logprobs": 20
	}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt_logprobs")
}

func TestNormalizeChatRequestStripsMinTokensWhenStopTokenIdsPresent(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"stop_token_ids": [163586, 9999999],
		"min_tokens": 1
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	_, exists := raw["min_tokens"]
	require.False(t, exists)
}

func TestNormalizeChatRequestConditionalMinTokensRuleTrueBranch(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"stop_token_ids": [7],
		"min_tokens": 3
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "min_tokens")
}

func TestNormalizeChatRequestKeepsMinTokensWithoutStopTokenIds(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"min_tokens": 5
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 5, raw["min_tokens"])
}

func TestNormalizeChatRequestConditionalMinTokensRuleFalseBranch(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"min_tokens": 3
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 3, raw["min_tokens"])
}

func TestNormalizeChatRequestStripsEmptyTools(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"tool_choice": "auto",
		"tools": [],
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "tools")
	require.NotContains(t, raw, "tool_choice")
}

func TestNormalizeChatRequestKeepsToolChoiceAutoWithTools(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"tool_choice": "auto",
		"tools": [{"type": "function", "function": {"name": "x", "description": "x", "parameters": {"type": "object"}}}],
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, "auto", raw["tool_choice"])
	require.Contains(t, raw, "tools")
}

func TestNormalizeChatRequestDefaultsToolChoiceToAutoWhenToolsProvided(t *testing.T) {
	// When the client passes `tools` without `tool_choice`, the gateway substitutes the
	// OpenAI-spec default ("auto") so downstream vLLM never sees an absent value -- vLLM's
	// own default routes through code that requires --enable-auto-tool-choice, and 66
	// captured failures showed clients consistently dropping the field.
	body := `{
		"tools": [{"type": "function", "function": {"name": "x", "parameters": {"type": "object"}}}],
		"messages": [{"role": "user", "content": "hi"}]
	}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.Equal(t, "auto", raw["tool_choice"])
}

func TestNormalizeChatRequestCoercesRequiredToAuto(t *testing.T) {
	body := `{
		"tool_choice": "required",
		"tools": [{"type":"function","function":{"name":"x","parameters":{"type":"object"}}}],
		"messages": [{"role":"user","content":"hi"}]
	}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.Equal(t, "auto", raw["tool_choice"])
}

func TestNormalizeChatRequestRejectsMalformedToolChoice(t *testing.T) {
	tests := []string{
		`{"tool_choice":"force","messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":42,"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":true,"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":["auto"],"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":{"type":"plugin","function":{"name":"x"}},"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":{"type":"function"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":{"type":"function","function":{}},"messages":[{"role":"user","content":"hi"}]}`,
		`{"tool_choice":{"type":"function","function":{"name":""}},"messages":[{"role":"user","content":"hi"}]}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(body))
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
			require.Contains(t, err.Error(), "tool_choice")
		})
	}
}

func TestNormalizeChatRequestKeepsExplicitToolChoiceValues(t *testing.T) {
	choices := []string{`"auto"`, `"none"`}
	for _, tc := range choices {
		t.Run(tc, func(t *testing.T) {
			body := `{
				"tool_choice": ` + tc + `,
				"tools": [{"type": "function", "function": {"name": "x", "parameters": {"type": "object"}}}],
				"messages": [{"role": "user", "content": "hi"}]
			}`
			out, _, err := normalizeChatRequest([]byte(body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.Equal(t, strings.Trim(tc, `"`), raw["tool_choice"])
		})
	}

	t.Run("function object", func(t *testing.T) {
		body := `{
			"tool_choice": {"type":"function","function":{"name":"x"}},
			"tools": [{"type": "function", "function": {"name": "x", "parameters": {"type": "object"}}}],
			"messages": [{"role": "user", "content": "hi"}]
		}`
		out, _, err := normalizeChatRequest([]byte(body))
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		choice, ok := raw["tool_choice"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "function", choice["type"])
	})
}

func TestNormalizeChatRequestStripsEmptyBadWords(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"bad_words": ["", "   ", "keep"],
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, []any{"keep"}, raw["bad_words"])

	body, _, err = normalizeChatRequest([]byte(`{
		"bad_words": ["", "\t", "\n"],
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)
	raw = map[string]any{}
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "bad_words")
}

func TestNormalizeChatRequestStripsWhitespaceOnlyBadWordsResearchCases(t *testing.T) {
	tests := []struct {
		name     string
		badWords string
		want     []any
	}{
		{name: "empty string", badWords: `[""]`},
		{name: "empty then keep", badWords: `["", "foo"]`, want: []any{"foo"}},
		{name: "keep then empty", badWords: `["foo", ""]`, want: []any{"foo"}},
		{name: "ascii space", badWords: `[" "]`},
		{name: "multiple empties", badWords: `["", "", ""]`},
		{name: "tab", badWords: `["\t"]`},
		{name: "line feed", badWords: `["\n"]`},
		{name: "non breaking space", badWords: `["\u00A0"]`},
		{name: "cjk space", badWords: `["\u3000"]`},
		{name: "carriage return", badWords: `["\r"]`},
		{name: "vertical tab", badWords: `["\u000B"]`},
		{name: "form feed", badWords: `["\u000C"]`},
		{name: "multi space", badWords: `["  "]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := normalizeChatRequest([]byte(`{
				"bad_words": ` + tt.badWords + `,
				"messages": [{"role": "user", "content": "hello"}]
			}`))
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			if tt.want == nil {
				require.NotContains(t, raw, "bad_words")
				return
			}
			require.Equal(t, tt.want, raw["bad_words"])
		})
	}
}

func TestNormalizeChatRequestKeepsSafeBadWordsResearchCases(t *testing.T) {
	tests := []struct {
		name     string
		badWords string
		want     []any
	}{
		{name: "simple token", badWords: `["foo"]`, want: []any{"foo"}},
		{name: "empty list", badWords: `[]`},
		{name: "single character", badWords: `["a"]`, want: []any{"a"}},
		{name: "two words", badWords: `["foo", "bar"]`, want: []any{"foo", "bar"}},
		{name: "zero width space", badWords: `["\u200B"]`, want: []any{"\u200B"}},
		{name: "nul", badWords: `["\u0000"]`, want: []any{"\u0000"}},
		{name: "bom", badWords: `["\uFEFF"]`, want: []any{"\uFEFF"}},
		{name: "zero width joiner", badWords: `["\u200D"]`, want: []any{"\u200D"}},
		{name: "zero width non joiner", badWords: `["\u200C"]`, want: []any{"\u200C"}},
		{name: "combining mark", badWords: `["\u0301"]`, want: []any{"\u0301"}},
		{name: "variation selector", badWords: `["\uFE0F"]`, want: []any{"\uFE0F"}},
		{name: "left padded", badWords: `[" a"]`, want: []any{" a"}},
		{name: "right padded", badWords: `["a "]`, want: []any{"a "}},
		{name: "emoji", badWords: `["😀"]`, want: []any{"😀"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := normalizeChatRequest([]byte(`{
				"bad_words": ` + tt.badWords + `,
				"messages": [{"role": "user", "content": "hello"}]
			}`))
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			if tt.want == nil {
				require.NotContains(t, raw, "bad_words")
				return
			}
			require.Equal(t, tt.want, raw["bad_words"])
		})
	}
}

func TestNormalizeChatRequestStripsNonFiniteSamplingValues(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"temperature": "nan",
		"top_p": "inf",
		"min_p": "-inf",
		"repetition_penalty": "infinity",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "temperature")
	require.NotContains(t, raw, "top_p")
	require.NotContains(t, raw, "min_p")
	require.NotContains(t, raw, "repetition_penalty")
}

func TestNormalizeChatRequestParsesStringEncodedSamplingValues(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"temperature": "1.2",
		"top_p": "0.5",
		"top_k": "40",
		"min_p": "0.1",
		"repetition_penalty": "1.2",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 1.2, raw["temperature"])
	require.EqualValues(t, 0.5, raw["top_p"])
	require.EqualValues(t, 40, raw["top_k"])
	require.EqualValues(t, 0.1, raw["min_p"])
	require.EqualValues(t, 1.2, raw["repetition_penalty"])
}

func TestNormalizeChatRequestStripsInvalidStringEncodedSamplingValues(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"temperature": "wat",
		"top_p": "",
		"top_k": "1.2.3",
		"min_p": "--1",
		"repetition_penalty": "not-a-number",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "temperature")
	require.NotContains(t, raw, "top_p")
	require.NotContains(t, raw, "top_k")
	require.NotContains(t, raw, "min_p")
	require.NotContains(t, raw, "repetition_penalty")
}

func TestNormalizeChatRequestSanitizesRepetitionPenalty(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"repetition_penalty": 5,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, MaxRepetitionPenalty, raw["repetition_penalty"])

	body, _, err = normalizeChatRequest([]byte(`{
		"repetition_penalty": "5.0",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)
	raw = map[string]any{}
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, MaxRepetitionPenalty, raw["repetition_penalty"])
}

func TestNormalizeChatRequestStripsOutOfRangeLogitBias(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"logit_bias": {"0": 1e30, "1": 10, "2": -101},
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, map[string]any{"1": float64(10)}, raw["logit_bias"])

	body, _, err = normalizeChatRequest([]byte(`{
		"logit_bias": {"0": 1e30},
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)
	raw = map[string]any{}
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "logit_bias")
}

func TestNormalizeChatRequestRejectsInvalidJSON(t *testing.T) {
	_, _, err := normalizeChatRequest([]byte(`{"messages":[`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse request")
}

func TestNormalizeForKimiMirrorsThinkingToTemplateKwargs(t *testing.T) {
	boolPtr := func(v bool) *bool {
		return &v
	}

	tests := []struct {
		name         string
		body         string
		model        string
		wantThinking *bool
		wantExtra    any
	}{
		{
			name:         "disabled",
			body:         `{"model":"moonshotai/Kimi-K2.6","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`,
			model:        kimiK26ModelID,
			wantThinking: boolPtr(false),
		},
		{
			name:         "enabled",
			body:         `{"model":"moonshotai/Kimi-K2.6","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hello"}]}`,
			model:        kimiK26ModelID,
			wantThinking: boolPtr(true),
		},
		{
			name:         "preserves other chat template kwargs",
			body:         `{"model":"moonshotai/Kimi-K2.6","thinking":{"type":"disabled"},"chat_template_kwargs":{"foo":"bar"},"messages":[{"role":"user","content":"hello"}]}`,
			model:        kimiK26ModelID,
			wantThinking: boolPtr(false),
			wantExtra:    "bar",
		},
		{
			name:         "explicit vllm thinking wins",
			body:         `{"model":"moonshotai/Kimi-K2.6","thinking":{"type":"enabled"},"chat_template_kwargs":{"thinking":false},"messages":[{"role":"user","content":"hello"}]}`,
			model:        kimiK26ModelID,
			wantThinking: boolPtr(false),
		},
		{
			name:  "non kimi unchanged",
			body:  `{"model":"Qwen/Test","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`,
			model: "Qwen/Test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := normalizeChatRequestForModel([]byte(tt.body), tt.model)
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			kwargs, hasKwargs := raw["chat_template_kwargs"].(map[string]any)
			if tt.wantThinking == nil {
				require.False(t, hasKwargs)
				return
			}
			require.True(t, hasKwargs)
			require.Equal(t, *tt.wantThinking, kwargs["thinking"])
			if tt.wantExtra != nil {
				require.Equal(t, tt.wantExtra, kwargs["foo"])
			}
		})
	}
}

func TestNormalizeChatRequestRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown top level field",
			body: `{"custom_param":true,"messages":[{"role":"user","content":"hello"}]}`,
			want: "custom_param",
		},
		{
			// JSON accepts "" as a key; the whitelist rejects it with a dedicated message
			name: "empty string key",
			body: `{"":"slip","messages":[{"role":"user","content":"hello"}]}`,
			want: "field with an empty name",
		},
		{
			name: "enforced tokens",
			body: `{"enforced_tokens":["x"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "enforced_tokens",
		},
		{
			name: "guided regex",
			body: `{"guided_regex":"[a-z]+","messages":[{"role":"user","content":"hello"}]}`,
			want: "guided_regex",
		},
		{
			name: "guided grammar",
			body: `{"guided_grammar":"root ::= item","messages":[{"role":"user","content":"hello"}]}`,
			want: "guided_grammar",
		},
		{
			name: "guided json",
			body: `{"guided_json":{"type":"object"},"messages":[{"role":"user","content":"hello"}]}`,
			want: "guided_json",
		},
		{
			name: "guided choice",
			body: `{"guided_choice":["a","b"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "guided_choice",
		},
		{
			name: "prompt logprobs",
			body: `{"prompt_logprobs":20,"messages":[{"role":"user","content":"hello"}]}`,
			want: "prompt_logprobs",
		},
		{
			name: "beam search",
			body: `{"use_beam_search":true,"messages":[{"role":"user","content":"hello"}]}`,
			want: "use_beam_search",
		},
		{
			name: "truncate prompt tokens",
			body: `{"truncate_prompt_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			want: "truncate_prompt_tokens",
		},
		{
			name: "allowed token ids",
			body: `{"allowed_token_ids":[1,2,3],"messages":[{"role":"user","content":"hello"}]}`,
			want: "allowed_token_ids",
		},
		{
			name: "ignore eos",
			body: `{"ignore_eos":true,"messages":[{"role":"user","content":"hello"}]}`,
			want: "ignore_eos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(tt.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
		})
	}
}

// Integration coverage that response_format is routed through the catalog rule and that pipeline
// errors are translated into HTTP 400. Exhaustive validator behavior lives in
// filters_parameters/response_format_test.go.
func TestNormalizeChatRequestResponseFormatPipeline(t *testing.T) {
	t.Run("accepts type text", func(t *testing.T) {
		body, _, err := normalizeChatRequest([]byte(`{"response_format":{"type":"text"},"messages":[{"role":"user","content":"hello"}]}`))
		require.NoError(t, err)
		require.Contains(t, string(body), `"response_format"`)
	})

	t.Run("accepts json_schema with simple schema", func(t *testing.T) {
		body, _, err := normalizeChatRequest([]byte(`{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object","properties":{"x":{"type":"string"}}}}},"messages":[{"role":"user","content":"hello"}]}`))
		require.NoError(t, err)
		require.Contains(t, string(body), `"response_format"`)
	})

	t.Run("rejects unknown type with HTTP 400", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"response_format":{"type":"banana"},"messages":[{"role":"user","content":"hello"}]}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "response_format")
		require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
	})

	t.Run("rejects pathological recursive schema with HTTP 400", func(t *testing.T) {
		deepSchema := `{"type":"object"}`
		for i := 0; i < 200; i++ {
			deepSchema = `{"type":"object","properties":{"x":` + deepSchema + `}}`
		}
		body := `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + deepSchema + `}},"messages":[{"role":"user","content":"hello"}]}`
		_, _, err := normalizeChatRequest([]byte(body))
		require.Error(t, err)
		require.Contains(t, err.Error(), "depth")
		require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
	})
}

func TestNormalizeChatRequestChatTemplateKwargsDepthBoundary(t *testing.T) {
	nestedChain := func(n int) string {
		s := `{}`
		for i := 1; i < n; i++ {
			s = `{"x":` + s + `}`
		}
		return s
	}

	t.Run("accepts chat_template_kwargs at depth limit", func(t *testing.T) {
		body := `{"chat_template_kwargs":` + nestedChain(16) + `,"messages":[{"role":"user","content":"hi"}]}`
		_, _, err := normalizeChatRequest([]byte(body))
		require.NoError(t, err)
	})

	t.Run("rejects chat_template_kwargs one level past limit with HTTP 400", func(t *testing.T) {
		body := `{"chat_template_kwargs":` + nestedChain(17) + `,"messages":[{"role":"user","content":"hi"}]}`
		_, _, err := normalizeChatRequest([]byte(body))
		require.Error(t, err)
		require.True(t, errors.Is(err, paramvalidators.ErrSchemaDepth),
			"expected ErrSchemaDepth (validator-layer reject), got: %v", err)
		require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
	})
}

func TestDefaultCatalogSchemaDepthLimits(t *testing.T) {
	const expected = 16

	findValidator := func(t *testing.T, name string) DocumentValidator {
		t.Helper()
		for _, p := range defaultParameterCatalog.parameters {
			if p.Name != name {
				continue
			}
			for _, rule := range p.Rules {
				h, ok := rule.Handler.(DocumentValidatorHandler)
				if !ok {
					continue
				}
				return h.Validator
			}
		}
		t.Fatalf("no DocumentValidator wired for parameter %q in defaultParameterCatalog", name)
		return nil
	}

	t.Run("response_format", func(t *testing.T) {
		v, ok := findValidator(t, "response_format").(paramvalidators.ResponseFormatValidator)
		require.True(t, ok, "response_format validator is not ResponseFormatValidator")
		require.Equal(t, expected, v.MaxDepth)
	})

	t.Run("tools", func(t *testing.T) {
		v, ok := findValidator(t, "tools").(paramvalidators.ToolsValidator)
		require.True(t, ok, "tools validator is not ToolsValidator")
		require.Equal(t, expected, v.MaxDepth)
	})

	t.Run("chat_template_kwargs", func(t *testing.T) {
		v, ok := findValidator(t, "chat_template_kwargs").(paramvalidators.ChatTemplateKwargsValidator)
		require.True(t, ok, "chat_template_kwargs validator is not ChatTemplateKwargsValidator")
		require.Equal(t, expected, v.MaxDepth)
	})
}

func TestNormalizeChatRequestEnforcesListCaps(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "stop too many entries",
			body: `{"stop":[` + strings.Repeat(`"a",`, 16) + `"b"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "stop",
		},
		{
			name: "stop entry too long",
			body: `{"stop":["` + strings.Repeat("a", 257) + `"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "stop[0]",
		},
		{
			name: "stop_token_ids too many entries",
			body: `{"stop_token_ids":[` + strings.Repeat(`1,`, 64) + `2],"messages":[{"role":"user","content":"hello"}]}`,
			want: "stop_token_ids",
		},
		{
			name: "bad_words too many entries",
			body: `{"bad_words":[` + strings.Repeat(`"a",`, 64) + `"b"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "bad_words",
		},
		{
			name: "bad_words entry too long",
			body: `{"bad_words":["` + strings.Repeat("a", 129) + `"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "bad_words[0]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(tt.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
		})
	}
}

func TestNormalizeChatRequestEnforcesMessagesCountCap(t *testing.T) {
	// Build a body with 2049 minimal valid user messages -- one over the cap.
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := 0; i < 2049; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":"hi"}`)
	}
	b.WriteString(`]}`)

	_, _, err := normalizeChatRequest([]byte(b.String()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "messages")
	require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
}

func TestNormalizeChatRequestAcceptsMessagesAtCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := 0; i < 2048; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":"hi"}`)
	}
	b.WriteString(`]}`)

	_, _, err := normalizeChatRequest([]byte(b.String()))
	require.NoError(t, err)
}

func TestNormalizeChatRequestValidatesSeed(t *testing.T) {
	t.Run("accepts non-negative integer", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"seed":42,"messages":[{"role":"user","content":"hello"}]}`))
		require.NoError(t, err)
	})
	t.Run("accepts absent seed", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
		require.NoError(t, err)
	})
	t.Run("rejects negative seed", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"seed":-5,"messages":[{"role":"user","content":"hello"}]}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "seed")
		require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
	})
	t.Run("rejects float seed", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"seed":3.14,"messages":[{"role":"user","content":"hello"}]}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "seed")
	})
	t.Run("rejects string seed", func(t *testing.T) {
		_, _, err := normalizeChatRequest([]byte(`{"seed":"42","messages":[{"role":"user","content":"hello"}]}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "seed")
	})
}

func TestNormalizeChatRequestEnforcesLogitBiasMapCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"logit_bias":{`)
	for i := 0; i < 1025; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":1`)
	}
	b.WriteString(`},"messages":[{"role":"user","content":"hello"}]}`)

	_, _, err := normalizeChatRequest([]byte(b.String()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "logit_bias")
	require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
}

func TestNormalizeChatRequestAcceptsListCapsAtLimit(t *testing.T) {
	t.Run("stop at exact entry limit", func(t *testing.T) {
		body := `{"stop":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 16), ",") + `],"messages":[{"role":"user","content":"hello"}]}`
		_, _, err := normalizeChatRequest([]byte(body))
		require.NoError(t, err)
	})
	t.Run("stop_token_ids at exact entry limit", func(t *testing.T) {
		body := `{"stop_token_ids":[` + strings.TrimSuffix(strings.Repeat(`1,`, 64), ",") + `],"messages":[{"role":"user","content":"hello"}]}`
		_, _, err := normalizeChatRequest([]byte(body))
		require.NoError(t, err)
	})
}

func TestNormalizeChatRequestRejectsMalformedMessages(t *testing.T) {
	tests := []string{
		`{"messages":"hello"}`,
		`{"messages":[]}`,
		`{"messages":[{"content":"hello"}]}`,
		`{"messages":[{"role":"user","content":123}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"missing","content":"hello"}]}`,
	}

	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(body))
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
		})
	}
}

func TestPrepareChatRequestBodyNormalizesTextContentParts(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hello"},
				{"type": "text", "text": "world"}
			]
		}]
	}`))

	body, _, err := prepareChatRequestBody(req)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	messages := raw["messages"].([]any)
	message := messages[0].(map[string]any)
	require.Equal(t, "hello\nworld", message["content"])
}

func TestPrepareChatRequestBodyNormalizesEmptyAssistantToolCallContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: `""`},
		{name: "whitespace", content: `" "`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
				"messages": [
					{"role": "user", "content": "What is 2+2?"},
					{"role": "assistant", "content": `+tt.content+`, "tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "web_search", "arguments": "{\"query\":\"2+2\"}"}
					}]},
					{"role": "tool", "content": "4", "tool_call_id": "call_1"}
				]
			}`))

			body, _, err := prepareChatRequestBody(req)
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			messages := raw["messages"].([]any)
			assistant := messages[1].(map[string]any)
			require.Contains(t, assistant, "content")
			require.Nil(t, assistant["content"])
		})
	}
}

func TestPrepareChatRequestBodyNormalizesEmptyToolContent(t *testing.T) {
	tests := []struct {
		name         string
		contentField string
	}{
		{name: "empty", contentField: `"content": "",`},
		{name: "whitespace", contentField: `"content": " ",`},
		{name: "null", contentField: `"content": null,`},
		{name: "missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
				"messages": [
					{"role": "user", "content": "What is 2+2?"},
					{"role": "assistant", "content": null, "tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "web_search", "arguments": "{\"query\":\"2+2\"}"}
					}]},
					{"role": "tool", `+tt.contentField+` "tool_call_id": "call_1"}
				]
			}`))

			body, _, err := prepareChatRequestBody(req)
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			messages := raw["messages"].([]any)
			tool := messages[2].(map[string]any)
			require.Equal(t, emptyToolResultContent, tool["content"])
		})
	}
}

// Explicit JSON `null` for `tool_calls` / `function_call` is treated as field-absent.
// Several OpenAI-SDK serializers emit null for empty slots; rejecting was a false-positive.
func TestNormalizeChatRequestTreatsNullToolCallsAndFunctionCallAsAbsent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "tool_calls null on assistant",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","tool_calls":null}
			]}`,
		},
		{
			name: "function_call null on assistant",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","function_call":null}
			]}`,
		},
		{
			name: "both null on assistant",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","tool_calls":null,"function_call":null}
			]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			messages := raw["messages"].([]any)
			assistant := messages[1].(map[string]any)
			require.NotContains(t, assistant, "tool_calls")
			require.NotContains(t, assistant, "function_call")
		})
	}
}

// `name` on role:"tool" messages is a legacy artifact of the role:"function" API and many
// SDKs still emit it; gateway silently strips so the request reaches vLLM in the modern
// shape without surfacing a 400 to the client.
func TestNormalizeChatRequestStripsLegacyNameFromToolMessages(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"what is 2+2?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"calc","arguments":"{\"a\":2,\"b\":2}"}}]},
		{"role":"tool","name":"calc","content":"4","tool_call_id":"call_1"}
	]}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	messages := raw["messages"].([]any)
	tool := messages[2].(map[string]any)
	require.NotContains(t, tool, "name")
	require.Equal(t, "4", tool["content"])
	require.Equal(t, "call_1", tool["tool_call_id"])
}

// Lock-in: removing the outer `normalizeContent` collapsed two code paths into one. The
// test helper (normalizeChatRequest) and the HTTP-boundary entrypoint (prepareChatRequestBody)
// must produce byte-identical output for every shape the message normalizer touches —
// otherwise we re-introduce the class of bugs "tests pass, production rejects".
func TestNormalizeChatRequestAndPrepareChatRequestBodyAgreeOnMessageShapes(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{
			name: "explicit-null tool_calls and function_call",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","tool_calls":null,"function_call":null}
			]}`,
		},
		{
			name: "legacy name on tool role",
			body: `{"messages":[
				{"role":"user","content":"q"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"calc","arguments":"{}"}}]},
				{"role":"tool","name":"calc","content":"42","tool_call_id":"c1"}
			]}`,
		},
		{
			name: "typed text content parts flattened",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"world"}]}]}`,
		},
		{
			name: "empty tool content normalized to sentinel",
			body: `{"messages":[
				{"role":"user","content":"q"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","content":"","tool_call_id":"c1"}
			]}`,
		},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			pipelineOut, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			boundaryOut, _, err := prepareChatRequestBody(req)
			require.NoError(t, err)

			var pipelineMessages, boundaryMessages any
			var pipelineRaw, boundaryRaw map[string]any
			require.NoError(t, json.Unmarshal(pipelineOut, &pipelineRaw))
			require.NoError(t, json.Unmarshal(boundaryOut, &boundaryRaw))
			pipelineMessages = pipelineRaw["messages"]
			boundaryMessages = boundaryRaw["messages"]
			require.Equal(t, pipelineMessages, boundaryMessages,
				"normalizeChatRequest and prepareChatRequestBody must agree on the messages shape")
		})
	}
}

// Empty-array assistant content is a legitimate "only a tool call, no prose" payload that
// some SDK bridges (Anthropic↔OpenAI in particular) emit instead of null / "". Treat it
// the same as null when a call payload is present; otherwise the validator still rejects.
// Captured-requests April 2026 batch: req-1779259426225387173-15356.
func TestNormalizeChatRequestNormalizesEmptyArrayAssistantContentWithToolCalls(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"explore the codebase"},
		{"role":"assistant","content":[],"tool_calls":[{"id":"c1","type":"function","function":{"name":"Task","arguments":"{\"prompt\":\"x\"}"}}]}
	]}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	messages := raw["messages"].([]any)
	assistant := messages[1].(map[string]any)
	require.Contains(t, assistant, "content")
	require.Nil(t, assistant["content"], "empty-array content should be normalized to nil for tool-call-only assistant turn")
	require.Contains(t, assistant, "tool_calls")
}

func TestNormalizeChatRequestNormalizesEmptyArrayContentForToolRole(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"q"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","content":[],"tool_call_id":"c1"}
	]}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	messages := raw["messages"].([]any)
	tool := messages[2].(map[string]any)
	require.Equal(t, emptyToolResultContent, tool["content"])
}

// Empty-array content on an assistant with NO tool_calls / function_call is silently
// dropped by the empty-assistant-turn normalizer (see TestNormalizeChatRequestDropsEmptyAssistantTurns
// for the full coverage). This used to be a hard rejection; we relaxed it to match the
// lenient policy for session-resume artifacts.
func TestNormalizeChatRequestDropsEmptyArrayContentOnPureAssistant(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"q"},
		{"role":"assistant","content":[]}
	]}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	messages := raw["messages"].([]any)
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0].(map[string]any)["role"])
}

// Orphan tool messages (tool_call_id with no matching prior assistant.tool_calls.id) are
// silently dropped. The strict OpenAI-spec rejection killed long agent conversations when
// client-side history compaction lost part of the assistant.tool_calls fan-out. The
// surviving (non-orphan) tool responses still pass the linkage check.
// Captured-requests April 2026 batch: req-1779263614553842074-24798 (and 32 similar).
func TestNormalizeChatRequestDropsOrphanToolMessages(t *testing.T) {
	t.Run("single orphan after valid pair", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"valid","tool_call_id":"c1"},
			{"role":"tool","content":"orphan","tool_call_id":"never_emitted"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 3, "orphan should be dropped, valid pair survives")
		require.Equal(t, "valid", messages[2].(map[string]any)["content"])
	})

	t.Run("multiple consecutive orphans dropped", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"r1","tool_call_id":"c1"},
			{"role":"tool","content":"orphan1","tool_call_id":"missing_a"},
			{"role":"tool","content":"orphan2","tool_call_id":"missing_b"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 3)
		require.Equal(t, "r1", messages[2].(map[string]any)["content"])
	})

	t.Run("orphan before any assistant turn is dropped", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"tool","content":"orphan","tool_call_id":"early"},
			{"role":"assistant","content":"answer"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 2)
		require.Equal(t, "user", messages[0].(map[string]any)["role"])
		require.Equal(t, "assistant", messages[1].(map[string]any)["role"])
	})

	t.Run("duplicate tool response for same id keeps first, drops second", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"first","tool_call_id":"c1"},
			{"role":"tool","content":"dup","tool_call_id":"c1"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 3)
		require.Equal(t, "first", messages[2].(map[string]any)["content"])
	})

	t.Run("two-of-three fan-out where one tool_call lost on client", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},
				{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}
			]},
			{"role":"tool","content":"r1","tool_call_id":"c1"},
			{"role":"tool","content":"r2","tool_call_id":"c2"},
			{"role":"tool","content":"r3-orphan","tool_call_id":"c3_lost"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 4)
	})

	t.Run("no orphans — pass-through unchanged", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"r1","tool_call_id":"c1"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 3)
	})
}

// Duplicate tool_calls[].id within a single assistant message is rejected — matches the
// OpenAI Chat Completions spec ([API reference](https://platform.openai.com/docs/api-reference/chat/create))
// and what every mainstream provider does (OpenAI server: HTTP 400 "Duplicate value for 'tool_call_id'";
// Anthropic / Bedrock: ValidationException). The duplicates have been observed coming from a model-side
// emission bug in Kimi-K2.6's vLLM tool parser ([PR #21259 review thread](https://github.com/vllm-project/vllm/pull/21259) —
// `history_tool_call_cnt` recomputed inside the per-choice loop with n>1 can collide). Moonshot's
// canonical fix ([tool_call_guidance.md](https://huggingface.co/moonshotai/Kimi-K2-Thinking/blob/main/docs/tool_call_guidance.md))
// is client-side ID rewrite to the trained-distribution `functions.<name>:<global_idx>` form, not
// silent gateway-side dedup. Lenient gateway behavior (drop / rename) risks information loss when
// the agent has multiple real tool results keyed by the duplicated id — verified in captured-requests
// May 2026 batch (req-1779369319274519506-325651: two distinct tool messages with `tool_call_id =
// "functions.ReadCommandOutput:2"`).
func TestNormalizeChatRequestRejectsDuplicateToolCallIDs(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"q"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"functions.X:2","type":"function","function":{"name":"X","arguments":"{\"a\":1}"}},
			{"id":"functions.X:2","type":"function","function":{"name":"X","arguments":"{\"a\":2}"}}
		]}
	]}`)
	_, _, err := normalizeChatRequest(body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool_calls[1].id is duplicated")
}

// Empty assistant turns — placeholders left by session-resume frameworks when the prose
// response between a tool result and the next user turn gets lost — are silently dropped.
// The literal `{"role":"assistant"}` carries no information for the model.
// Captured-requests April 2026: req-1779291749005921857-69735 (and 1 sibling).
func TestNormalizeChatRequestDropsEmptyAssistantTurns(t *testing.T) {
	t.Run("literal {role:assistant} between tool and user", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"r","tool_call_id":"c1"},
			{"role":"assistant"},
			{"role":"user","content":"continue"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 4)
		require.Equal(t, "tool", messages[2].(map[string]any)["role"])
		require.Equal(t, "user", messages[3].(map[string]any)["role"])
	})

	t.Run("assistant with content:null and no calls dropped", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null},
			{"role":"user","content":"q2"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 2)
	})

	t.Run("assistant with empty-string content and no calls dropped", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":""},
			{"role":"user","content":"q2"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 2)
	})

	t.Run("assistant with empty-array content and no calls dropped", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":[]},
			{"role":"user","content":"q2"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 2)
	})

	t.Run("assistant with tool_calls is preserved", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","content":"r","tool_call_id":"c1"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 3, "assistant with tool_calls is meaningful, never dropped")
	})

	t.Run("assistant with content is preserved", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":"answer"}
		]}`)
		out, _, err := normalizeChatRequest(body)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		require.Len(t, messages, 2)
	})
}

// `think: bool` is the Ollama-style top-level reasoning flag, used by Cline / Ollama-CLI
// derived tools that target multiple backends. No vLLM-served model on the gateway
// today is reasoning-capable, so silent-strip mirrors the treatment of `service_tier`,
// `store`, and `thinking_config`. Revisit if a reasoning-capable route is added.
// Captured-requests April 2026: 5 captures from Cline-like clients on Kimi K2.6 / Qwen3.
func TestNormalizeChatRequestSilentlyStripsThinkParameter(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "think false", body: `{"think":false,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "think true", body: `{"think":true,"messages":[{"role":"user","content":"hi"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "think")
		})
	}
}

// When a request hits multiple rejection paths at once (unsupported top-level parameter
// AND malformed message content), the pipeline now surfaces the parameter rejection first
// (PreValidation runs before NormalizeDocument). Both still produce a 400 — the test pins
// the *behavior* (one error fires, request is rejected) rather than the exact priority,
// so a future re-order won't silently break it.
func TestPrepareChatRequestBodyRejectsCombinationOfUnsupportedFieldAndBadContent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"unsupported_field": true,
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]
	}`))
	_, _, err := prepareChatRequestBody(req)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))

	msg := err.Error()
	switch {
	case strings.Contains(msg, "unsupported_field"):
		// Current behavior: parameter rules run first in PreValidation, this error wins.
	case strings.Contains(msg, "image_url"):
		// Acceptable fallback: a future re-order surfaces the content-shape error first.
	default:
		t.Fatalf("expected either unsupported_field or image_url in error, got: %v", err)
	}
}

// Coverage at the HTTP boundary for the two recent lenience fixes — same expectations as
// the test-helper variants above, but exercised through the production code path so a
// future regression in the pipeline ordering can't silently sneak past.
func TestPrepareChatRequestBodyAppliesMessageLenienceFixes(t *testing.T) {
	t.Run("null tool_calls dropped, request accepted", func(t *testing.T) {
		body := `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"hello","tool_calls":null}
		]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		out, _, err := prepareChatRequestBody(req)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		assistant := messages[1].(map[string]any)
		require.NotContains(t, assistant, "tool_calls")
	})

	t.Run("legacy name stripped from tool message", func(t *testing.T) {
		body := `{"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"calc","arguments":"{}"}}]},
			{"role":"tool","name":"calc","content":"42","tool_call_id":"c1"}
		]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		out, _, err := prepareChatRequestBody(req)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(out, &raw))
		messages := raw["messages"].([]any)
		tool := messages[2].(map[string]any)
		require.NotContains(t, tool, "name")
		require.Equal(t, "42", tool["content"])
	})
}

func TestPrepareChatRequestBodyRejectsNonTextContentParts(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "image_url",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
			want: `unsupported value "image_url"`,
		},
		{
			name: "input_text",
			body: `{"messages":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			want: `unsupported value "input_text"`,
		},
		{
			name: "unknown",
			body: `{"messages":[{"role":"user","content":[{"type":"custom","text":"hello"}]}]}`,
			want: `unsupported value "custom"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			_, _, err := prepareChatRequestBody(req)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, http.StatusInternalServerError))
		})
	}
}

func TestPrepareChatRequestBodyAllowsExtraFieldsOnTextContentParts(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "hello",
				"cache_control": {"type": "ephemeral"}
			}]
		}]
	}`))

	body, _, err := prepareChatRequestBody(req)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	messages := raw["messages"].([]any)
	message := messages[0].(map[string]any)
	require.Equal(t, "hello", message["content"])
}

func TestPrepareChatRequestBodyRejectsBodiesLargerThanTenMiB(t *testing.T) {
	tooLarge := bytes.Repeat([]byte("a"), MaxChatRequestBodySize+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(tooLarge))

	_, _, err := prepareChatRequestBody(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "request body too large")
	require.Equal(t, http.StatusRequestEntityTooLarge, chatRequestErrorStatus(err, http.StatusBadRequest))
}

func TestPrepareChatRequestBodyAcceptsTenMiBBody(t *testing.T) {
	paddingSize := MaxChatRequestBodySize - len(`{"messages":[{"role":"user","content":""}]}`)
	body := `{"messages":[{"role":"user","content":"` + strings.Repeat("a", paddingSize) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	_, _, err := prepareChatRequestBody(req)
	require.NoError(t, err)
}

func TestEnsureRequestNestingDepth(t *testing.T) {
	// nestedObjects produces `{"a":{"a":...}}` of the given object-nesting depth.
	nestedObjects := func(depth int) []byte {
		return []byte(strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth))
	}

	t.Run("at limit accepted", func(t *testing.T) {
		require.NoError(t, ensureRequestNestingDepth(nestedObjects(MaxRequestNestingDepth), MaxRequestNestingDepth))
	})

	t.Run("one over limit rejected", func(t *testing.T) {
		err := ensureRequestNestingDepth(nestedObjects(MaxRequestNestingDepth+1), MaxRequestNestingDepth)
		require.Error(t, err)
		require.Contains(t, err.Error(), "request nesting depth exceeds limit")
	})

	t.Run("array nesting counts equally", func(t *testing.T) {
		body := []byte(strings.Repeat(`[`, MaxRequestNestingDepth+1) + `1` + strings.Repeat(`]`, MaxRequestNestingDepth+1))
		err := ensureRequestNestingDepth(body, MaxRequestNestingDepth)
		require.Error(t, err)
	})

	t.Run("braces inside strings do not count", func(t *testing.T) {
		// Without string-awareness, this body would appear to nest 100 deep.
		body := []byte(`{"k":"` + strings.Repeat(`{`, 100) + `"}`)
		require.NoError(t, ensureRequestNestingDepth(body, MaxRequestNestingDepth))
	})

	t.Run("escaped quote inside string", func(t *testing.T) {
		// The escaped quote must not exit string mode; the trailing braces inside the
		// string still must not be counted.
		body := []byte(`{"k":"x\"` + strings.Repeat(`{`, 100) + `"}`)
		require.NoError(t, ensureRequestNestingDepth(body, MaxRequestNestingDepth))
	})

	t.Run("imbalanced closers rebase to zero", func(t *testing.T) {
		// Excess closers must not let a later valid block bypass the limit by going negative.
		body := []byte(strings.Repeat(`}`, 50) + strings.Repeat(`{`, MaxRequestNestingDepth+1))
		err := ensureRequestNestingDepth(body, MaxRequestNestingDepth)
		require.Error(t, err)
	})
}

func TestNormalizeChatRequestRejectsBodyAtNestingLimit(t *testing.T) {
	// Pipeline-level proof that the pre-scan participates in normalizeChatRequest.
	deep := `"x"`
	for i := 0; i < MaxRequestNestingDepth+1; i++ {
		deep = `{"a":` + deep + `}`
	}
	body := `{"messages":[{"role":"user","content":` + deep + `}]}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "request nesting depth exceeds limit")
}

// Regression guard for the document mutex: without proper locking this trips
// Go's fatal "concurrent map writes" or the race detector.
func TestChatRequestDocumentConcurrentAccess(t *testing.T) {
	doc, err := decodeChatRequestDocument([]byte(`{"a":1,"b":2,"c":3}`))
	require.NoError(t, err)

	const workers = 32
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			key := "k" + strconv.Itoa(id)
			for i := 0; i < iterations; i++ {
				doc.Set(key, i)
				_, _ = doc.Get(key)
				_ = doc.Has("a")
				_, _ = doc.String("missing")
				switch i % 4 {
				case 0:
					_ = doc.Keys()
				case 1:
					_, _ = doc.Marshal()
				case 2:
					doc.RLockedScope(func(raw map[string]any) {
						for range raw {
						}
					})
				case 3:
					doc.LockedScope(func(raw map[string]any) {
						raw["shared"] = i
					})
				}
			}
			doc.Delete(key)
		}(w)
	}
	wg.Wait()
}

// End-to-end coverage that the four OpenAI Chat Completions observability fields survive
// the catalog's unknown-key gate, with `metadata` bounded and `stream_options` sanitized.
// Without these catalog entries the gate would 400 every legitimate OpenAI-built client
// (official SDK with `user=...`, LangChain with `metadata={...}`, any streaming client
// asking for final-chunk usage).
func TestNormalizeChatRequestAcceptsOpenAIObservabilityFields(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"user":"alice",
		"metadata":{"trace_id":"abc","span_id":"def"},
		"parallel_tool_calls":false,
		"stream":true,
		"stream_options":{"include_usage":true}
	}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.Equal(t, "alice", raw["user"])
	require.Equal(t, map[string]any{"trace_id": "abc", "span_id": "def"}, raw["metadata"])
	require.Equal(t, false, raw["parallel_tool_calls"])
	so, ok := raw["stream_options"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, so["include_usage"])
}

// Pipeline-level coverage of StreamOptionsValidator: `continuous_usage_stats` drops out
// (vLLM-project/vllm#9028), `include_usage` survives.
func TestNormalizeChatRequestStripsContinuousUsageStats(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":true,"continuous_usage_stats":true}
	}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	so := raw["stream_options"].(map[string]any)
	require.Equal(t, true, so["include_usage"])
	require.NotContains(t, so, "continuous_usage_stats")
}

// Pipeline-level coverage: stream_options that empties out after sanitize is dropped.
func TestNormalizeChatRequestDropsEmptiedStreamOptions(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"continuous_usage_stats":true}
	}`)
	out, _, err := normalizeChatRequest(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "stream_options")
}

// stream_options is meaningless without streaming — gateway strips it silently when
// `stream` is not `true` (absent, false, or non-bool). Prevents clients from accidentally
// shipping a streaming-only option on a non-streaming request.
func TestNormalizeChatRequestStripsStreamOptionsWhenStreamFalseOrAbsent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "stream absent", body: `{"messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true}}`},
		{name: "stream false", body: `{"messages":[{"role":"user","content":"hi"}],"stream":false,"stream_options":{"include_usage":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "stream_options")
		})
	}
}

// Pipeline-level coverage of MetadataValidator: too many keys / oversize values are
// rejected.
func TestNormalizeChatRequestRejectsOversizedMetadata(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"metadata":{"k":"` + strings.Repeat("v", 513) + `"}
	}`)
	_, _, err := normalizeChatRequest(body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "metadata")
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetDefaultsForKimi(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":2048`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetRespectsClientValue(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"thinking_token_budget":500}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":500`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetClampsAboveMaxTokens(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"thinking_token_budget":10000}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":4096`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetClampsAboveAbsoluteMax(t *testing.T) {
	oldCap := RequestMaxTokensCap
	RequestMaxTokensCap = 200_000
	t.Cleanup(func() { RequestMaxTokensCap = oldCap })

	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":200000,"thinking_token_budget":150000}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":96000`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetSmallMaxTokensSplitsInHalf(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":200}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":100`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetHalfSplitMidRange(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":400}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":200`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetEnforcedEvenWhenDisabled(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"thinking":{"type":"disabled"}}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"thinking_token_budget":2048`)
}

func TestNormalizeChatRequestKimiThinkingTokenBudgetNotInjectedForOtherModels(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096}`),
		"some/other-model",
	)
	require.NoError(t, err)
	require.NotContains(t, string(body), `thinking_token_budget`)
}

func TestNormalizeChatRequestThinkingTokenBudgetStrippedForOtherModelsEvenIfClientSet(t *testing.T) {
	body, _, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"thinking_token_budget":200000}`),
		"some/other-model",
	)
	require.NoError(t, err)
	require.NotContains(t, string(body), `thinking_token_budget`)
}

func TestNormalizeChatRequestKimiMaxTokensClampedBelow(t *testing.T) {
	for _, c := range []struct {
		in, want uint64
	}{
		{1, 16}, {8, 16}, {16, 16}, {100, 100},
	} {
		body := fmt.Sprintf(`{"messages":[{"role":"user","content":"x"}],"max_tokens":%d,"thinking_token_budget":0}`, c.in)
		out, req, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
		require.NoError(t, err)
		require.Contains(t, string(out), fmt.Sprintf(`"max_tokens":%d`, c.want))
		require.Contains(t, string(out), `"thinking_token_budget":0`)
		require.EqualValues(t, c.want, req.MaxTokens)
	}
}

func TestNormalizeChatRequestKimiMaxCompletionTokensClampedBelow(t *testing.T) {
	body, req, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_completion_tokens":1}`),
		kimiK26ModelID,
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"max_completion_tokens":16`)
	require.EqualValues(t, 16, req.MaxTokens)
}

func TestNormalizeChatRequestMaxTokensNotClampedForOtherModels(t *testing.T) {
	body, req, err := normalizeChatRequestForModel(
		[]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":1}`),
		"some/other-model",
	)
	require.NoError(t, err)
	require.Contains(t, string(body), `"max_tokens":1`)
	require.EqualValues(t, 1, req.MaxTokens)
}

// safety_identifier is forwarded to Kimi K2.6 (Moonshot consumes it for abuse tracking)
// and silently stripped for every other model (no documented downstream consumer).
func TestNormalizeChatRequestForwardsSafetyIdentifierForKimi(t *testing.T) {
	body := []byte(`{
		"model":"moonshotai/Kimi-K2.6",
		"messages":[{"role":"user","content":"hi"}],
		"safety_identifier":"hashed-user-abc"
	}`)
	out, _, err := normalizeChatRequestForModel(body, kimiK26ModelID)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.Equal(t, "hashed-user-abc", raw["safety_identifier"])
}

func TestNormalizeChatRequestStripsSafetyIdentifierForOtherModels(t *testing.T) {
	body := []byte(`{
		"model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
		"messages":[{"role":"user","content":"hi"}],
		"safety_identifier":"hashed-user-abc"
	}`)
	out, _, err := normalizeChatRequestForModel(body, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8")
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "safety_identifier")
}

// Kimi K2.6 still rejects safety_identifier when it violates the shape contract
// (non-string or over the 512 B cap) — gate runs validation, not blind pass-through.
func TestNormalizeChatRequestRejectsMalformedSafetyIdentifierForKimi(t *testing.T) {
	tests := []string{
		`{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"safety_identifier":42}`,
		`{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"safety_identifier":{}}`,
		`{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"safety_identifier":"` + strings.Repeat("x", 513) + `"}`,
	}
	for _, body := range tests {
		t.Run(body[:80], func(t *testing.T) {
			_, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
			require.Error(t, err)
			require.Contains(t, err.Error(), "safety_identifier")
		})
	}
}

func TestNormalizeChatRequestStripsSilentDropFields(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "service_tier", body: `{"messages":[{"role":"user","content":"hi"}],"service_tier":"flex"}`, field: "service_tier"},
		{name: "store true", body: `{"messages":[{"role":"user","content":"hi"}],"store":true}`, field: "store"},
		{name: "store false", body: `{"messages":[{"role":"user","content":"hi"}],"store":false}`, field: "store"},
		{name: "provider object", body: `{"messages":[{"role":"user","content":"hi"}],"provider":{"order":["openai","anthropic"]}}`, field: "provider"},
		{name: "plugins array", body: `{"messages":[{"role":"user","content":"hi"}],"plugins":[{"id":"web","max_results":5}]}`, field: "plugins"},
		{name: "prompt_cache_key", body: `{"messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"session-42"}`, field: "prompt_cache_key"},
		{name: "cache_key (Moonshot Kimi context-cache hint)", body: `{"messages":[{"role":"user","content":"hi"}],"cache_key":"kimi-cli_f1c55293"}`, field: "cache_key"},
		{name: "extra_headers object", body: `{"messages":[{"role":"user","content":"hi"}],"extra_headers":{"x-trace-id":"abc"}}`, field: "extra_headers"},
		{name: "thinking_config object", body: `{"messages":[{"role":"user","content":"hi"}],"thinking_config":{"thinkingBudget":1000}}`, field: "thinking_config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tt.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, tt.field)
		})
	}
}

func TestNormalizeChatRequestUnwrapsExtraBodyThinkingForKimi(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"extra_body":{"thinking":{"type":"disabled"}}}`
	out, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "extra_body")
	kwargs, ok := raw["chat_template_kwargs"].(map[string]any)
	require.True(t, ok, "thinking must mirror to chat_template_kwargs for Kimi")
	require.Equal(t, false, kwargs["thinking"])
	require.NotContains(t, raw, "thinking", "top-level thinking dropped after mirror — vLLM chat template only consumes chat_template_kwargs")
}

func TestNormalizeChatRequestUnwrapsExtraBodyThinkingForNonKimi(t *testing.T) {
	body := `{"model":"Qwen/Test","messages":[{"role":"user","content":"hi"}],"extra_body":{"thinking":{"type":"enabled"}}}`
	out, _, err := normalizeChatRequestForModel([]byte(body), "Qwen/Test")
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "extra_body")
	thinking, ok := raw["thinking"].(map[string]any)
	require.True(t, ok, "thinking lifted to top-level even for non-Kimi")
	require.Equal(t, "enabled", thinking["type"])
	_, hasKwargs := raw["chat_template_kwargs"]
	require.False(t, hasKwargs, "no mirror to chat_template_kwargs for non-Kimi")
}

func TestNormalizeChatRequestExtraBodyTopLevelWinsOnConflict(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"extra_body":{"thinking":{"type":"enabled"}}}`
	out, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "extra_body")
	kwargs, _ := raw["chat_template_kwargs"].(map[string]any)
	require.Equal(t, false, kwargs["thinking"], "top-level thinking wins over extra_body")
}

func TestNormalizeChatRequestExtraBodyRejectsUnknownLiftedField(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"extra_body":{"weird_field":1}}`
	_, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.Error(t, err, "unknown lifted field must hit the unknown-parameter check")
	require.Contains(t, err.Error(), "weird_field")
}

func TestNormalizeChatRequestExtraBodyNonObjectSilentlyDropped(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "string", body: `{"messages":[{"role":"user","content":"hi"}],"extra_body":"thinking"}`},
		{name: "number", body: `{"messages":[{"role":"user","content":"hi"}],"extra_body":42}`},
		{name: "bool", body: `{"messages":[{"role":"user","content":"hi"}],"extra_body":true}`},
		{name: "null", body: `{"messages":[{"role":"user","content":"hi"}],"extra_body":null}`},
		{name: "array", body: `{"messages":[{"role":"user","content":"hi"}],"extra_body":[{"thinking":{"type":"enabled"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "extra_body")
			require.NotContains(t, raw, "thinking")
		})
	}
}

func TestNormalizeChatRequestExtraBodyDoesNotRecurse(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"extra_body":{"extra_body":{"thinking":{"type":"enabled"}}}}`
	out, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "extra_body", "inner extra_body must be dropped, not re-lifted")
	require.NotContains(t, raw, "thinking")
	_, hasKwargs := raw["chat_template_kwargs"]
	require.False(t, hasKwargs)
}

func TestNormalizeChatRequestExtraBodyEmptyObjectJustDrops(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"extra_body":{}}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "extra_body")
}

func TestNormalizeChatRequestStripsReasoningEffort(t *testing.T) {
	cases := []struct{ name, body string }{
		{name: "high", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`},
		{name: "none", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`},
		{name: "xhigh", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "reasoning_effort", "all routed models are non-reasoning today, field must be stripped")
		})
	}
}

func TestNormalizeChatRequestRejectsInvalidReasoningEffort(t *testing.T) {
	cases := []struct{ name, body string }{
		{name: "unknown enum", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`},
		{name: "non-string", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":5}`},
		{name: "empty", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(tc.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), "reasoning_effort")
		})
	}
}

func TestNormalizeChatRequestTranslatesReasoningObjectToEffort(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high","max_tokens":2000,"exclude":true}}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "reasoning", "wrapper removed — inner max_tokens/exclude dropped with it")
	require.NotContains(t, raw, "reasoning_effort", "lifted then stripped — non-reasoning routes")
	require.NotContains(t, raw, "exclude")
}

func TestNormalizeChatRequestReasoningEnabledFalseOverridesEffort(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"reasoning":{"enabled":false,"effort":"high"}}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "reasoning")
	require.NotContains(t, raw, "reasoning_effort")
}

func TestNormalizeChatRequestReasoningInvalidEffortRejected(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"max"}}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err, "invalid effort must surface from ReasoningEffortValidator after lift")
	require.Contains(t, err.Error(), "reasoning_effort")
}

func TestNormalizeChatRequestTranslatesEnableThinkingToChatTemplateKwargs(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  bool
	}{
		{name: "true", body: `{"messages":[{"role":"user","content":"hi"}],"enable_thinking":true}`, want: true},
		{name: "false", body: `{"messages":[{"role":"user","content":"hi"}],"enable_thinking":false}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(out, &raw))
			require.NotContains(t, raw, "enable_thinking")
			kwargs, ok := raw["chat_template_kwargs"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tc.want, kwargs["enable_thinking"])
		})
	}
}

func TestNormalizeChatRequestEnableThinkingPreservesExistingNested(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"enable_thinking":true,"chat_template_kwargs":{"enable_thinking":false}}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	require.NotContains(t, raw, "enable_thinking")
	kwargs, _ := raw["chat_template_kwargs"].(map[string]any)
	require.Equal(t, false, kwargs["enable_thinking"])
}

func TestNormalizeChatRequestRejectsNonBoolEnableThinking(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"enable_thinking":"true"}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "enable_thinking")
}

func TestNormalizeChatRequestReasoningLiftsNonStringEffortThenRejected(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":5}}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err, "non-string effort must be lifted then surface from ReasoningEffortValidator")
	require.Contains(t, err.Error(), "reasoning_effort")
}

func TestModelScopedParameterHandlerBranches(t *testing.T) {
	track := func(label string) ParameterHandler {
		return parameterHandlerFunc(func(ctx *RequestFilterContext, _ VLLMParameter) error {
			ctx.Document.Set("_branch", label)
			return nil
		})
	}
	cases := []struct {
		name        string
		handler     ModelScopedParameterHandler
		routedModel string
		want        string
	}{
		{
			name: "match runs Handler",
			handler: ModelScopedParameterHandler{
				Models:           []string{"kimi"},
				Handler:          track("matched"),
				UnmatchedHandler: track("unmatched"),
			},
			routedModel: "kimi",
			want:        "matched",
		},
		{
			name: "unmatched runs UnmatchedHandler",
			handler: ModelScopedParameterHandler{
				Models:           []string{"kimi"},
				Handler:          track("matched"),
				UnmatchedHandler: track("unmatched"),
			},
			routedModel: "qwen",
			want:        "unmatched",
		},
		{
			name: "nil Models always runs UnmatchedHandler",
			handler: ModelScopedParameterHandler{
				Models:           nil,
				UnmatchedHandler: track("unmatched"),
			},
			routedModel: "anything",
			want:        "unmatched",
		},
		{
			name: "match with nil Handler is a no-op",
			handler: ModelScopedParameterHandler{
				Models:           []string{"kimi"},
				UnmatchedHandler: track("unmatched"),
			},
			routedModel: "kimi",
			want:        "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := newRequestFilterContext([]byte(`{}`), false, defaultOutputTokenLimits())
			require.NoError(t, err)
			ctx.RoutedModel = tc.routedModel

			require.NoError(t, tc.handler.Apply(ctx, VLLMParameter{Name: "test"}))

			if tc.want == "" {
				require.False(t, ctx.Document.Has("_branch"))
				return
			}
			v, ok := ctx.Document.Get("_branch")
			require.True(t, ok)
			require.Equal(t, tc.want, v)
		})
	}
}

type parameterHandlerFunc func(*RequestFilterContext, VLLMParameter) error

func (f parameterHandlerFunc) Apply(ctx *RequestFilterContext, p VLLMParameter) error {
	return f(ctx, p)
}

func TestNormalizeChatRequestRejectsStructuredOutputsForKimi(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}],"structured_outputs":{"json_object":true}}`
	_, _, err := normalizeChatRequestForModel([]byte(body), kimiK26ModelID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "structured_outputs")
}

func TestNormalizeChatRequestAcceptsStructuredOutputsForOtherModels(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"structured_outputs":{"json":{"type":"object","properties":{"x":{"type":"string"}}}}}`
	out, _, err := normalizeChatRequestForModel([]byte(body), "Qwen/Test")
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	so, ok := raw["structured_outputs"].(map[string]any)
	require.True(t, ok, "field must reach the upstream after validation passes")
	require.Contains(t, so, "json")
}

func TestNormalizeChatRequestRejectsStructuredOutputsResponseFormatConflict(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"},"structured_outputs":{"json_object":true}}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "response_format")
}

func TestNormalizeChatRequestRejectsStructuredOutputsExactlyOneViolation(t *testing.T) {
	cases := []struct{ name, body string }{
		{name: "zero constraints", body: `{"messages":[{"role":"user","content":"hi"}],"structured_outputs":{"disable_any_whitespace":true}}`},
		{name: "two constraints", body: `{"messages":[{"role":"user","content":"hi"}],"structured_outputs":{"json":{"type":"string"},"regex":"\\d+"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := normalizeChatRequest([]byte(tc.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), "structured_outputs")
		})
	}
}

func TestNormalizeChatRequestRejectsStructuredOutputsDangerousGrammar(t *testing.T) {
	bombs := strings.Repeat("(", 201)
	body := `{"messages":[{"role":"user","content":"hi"}],"structured_outputs":{"grammar":"` + bombs + `"}}`
	_, _, err := normalizeChatRequest([]byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "structured_outputs.grammar")
}

func TestNormalizeChatRequestStructuredOutputsStripsPrivateFields(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"structured_outputs":{"json_object":true,"_backend":"xgrammar","_backend_was_auto":true}}`
	out, _, err := normalizeChatRequest([]byte(body))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	so, _ := raw["structured_outputs"].(map[string]any)
	require.NotContains(t, so, "_backend")
	require.NotContains(t, so, "_backend_was_auto")
}

// Locks in that structured_outputs is validated in PreValidation. PostLimits would put
// validation after max_tokens defaulting / n greedy-sampling rewrite, surfacing schema
// errors only after irrelevant rewrites have already mutated the request.
func TestStructuredOutputsCatalogEntryRunsInPreValidationStage(t *testing.T) {
	var found bool
	for _, p := range defaultParameterCatalog.parameters {
		if p.Name != "structured_outputs" {
			continue
		}
		found = true
		require.NotEmpty(t, p.Rules, "structured_outputs must declare at least one rule")
		for _, r := range p.Rules {
			require.Equalf(t, RequestFilterStagePreValidation, r.Stage,
				"structured_outputs rule must run in PreValidation, got stage %d", r.Stage)
		}
	}
	require.True(t, found, "structured_outputs catalog entry missing")
}
