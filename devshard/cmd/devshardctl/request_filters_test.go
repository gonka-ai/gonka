package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	body, req, err := normalizeChatRequestForAuthAndLimits([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), false, limits)
	require.NoError(t, err)
	require.EqualValues(t, 2_048, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":2048`)

	body, req, err = normalizeChatRequestForAuthAndLimits([]byte(`{"max_tokens":4096,"messages":[{"role":"user","content":"hello"}]}`), false, limits)
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

func TestNormalizeChatRequestStripsPromptLogprobs(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"prompt_logprobs": 20
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	_, exists := raw["prompt_logprobs"]
	require.False(t, exists)
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

func TestApplyKimiRequestOverrides(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		req            chatRequest
		model          string
		wantStream     bool
		wantToolChoice any
		wantStructured bool
	}{
		{
			name:           "explicit false",
			body:           `{"model":"moonshotai/Kimi-K2.6","stream":false,"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: kimiK26ModelID, Stream: false},
			model:          kimiK26ModelID,
			wantStream:     false,
			wantToolChoice: nil,
		},
		{
			name:           "missing stream",
			body:           `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: kimiK26ModelID, Stream: false},
			model:          kimiK26ModelID,
			wantStream:     false,
			wantToolChoice: nil,
		},
		{
			name:           "default kimi model",
			body:           `{"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Stream: false},
			model:          kimiK26ModelID,
			wantStream:     false,
			wantToolChoice: nil,
		},
		{
			name:           "already streaming",
			body:           `{"model":"moonshotai/Kimi-K2.6","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: kimiK26ModelID, Stream: true},
			model:          kimiK26ModelID,
			wantStream:     true,
			wantToolChoice: nil,
		},
		{
			name:           "tool auto preserved",
			body:           `{"model":"moonshotai/Kimi-K2.6","stream":true,"tool_choice":"auto","tools":[{"type":"function","function":{"name":"x","description":"x","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: kimiK26ModelID, Stream: true},
			model:          kimiK26ModelID,
			wantStream:     true,
			wantToolChoice: "auto",
		},
		{
			name:           "structured outputs removed",
			body:           `{"model":"moonshotai/Kimi-K2.6","stream":false,"structured_outputs":{"schema":{"type":"object"}},"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: kimiK26ModelID, Stream: false},
			model:          kimiK26ModelID,
			wantStream:     false,
			wantToolChoice: nil,
		},
		{
			name:           "non kimi unchanged",
			body:           `{"model":"Qwen/Test","stream":false,"structured_outputs":{"schema":{"type":"object"}},"messages":[{"role":"user","content":"hello"}]}`,
			req:            chatRequest{Model: "Qwen/Test", Stream: false},
			model:          "Qwen/Test",
			wantStream:     false,
			wantToolChoice: nil,
			wantStructured: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, req, err := applyKimiRequestOverrides([]byte(tt.body), tt.req, tt.model)
			require.NoError(t, err)
			require.Equal(t, tt.wantStream, req.Stream)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(body, &raw))
			if _, hasStream := raw["stream"]; hasStream {
				require.Equal(t, tt.wantStream, raw["stream"])
			}
			if tt.wantToolChoice == nil {
				require.NotContains(t, raw, "tool_choice")
			} else {
				require.Equal(t, tt.wantToolChoice, raw["tool_choice"])
			}
			if tt.wantStructured {
				require.Contains(t, raw, "structured_outputs")
			} else {
				require.NotContains(t, raw, "structured_outputs")
			}
		})
	}
}

func TestApplyKimiRequestOverridesTranslatesMoonshotThinkingForVLLM(t *testing.T) {
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
			name:  "invalid moonshot type is ignored",
			body:  `{"model":"moonshotai/Kimi-K2.6","thinking":{"type":"brief"},"messages":[{"role":"user","content":"hello"}]}`,
			model: kimiK26ModelID,
		},
		{
			name:  "non kimi unchanged",
			body:  `{"model":"Qwen/Test","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`,
			model: "Qwen/Test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := applyKimiRequestOverrides([]byte(tt.body), chatRequest{Model: tt.model}, tt.model)
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

func TestNormalizeChatRequestStripsStructuredOutputsBeforeValidation(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"model": "Qwen/Test",
		"structured_outputs": {"regex": "[a-z]+"},
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.NotContains(t, raw, "structured_outputs")
}

func TestNormalizeChatRequestKeepsStandardPenaltyFields(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"presence_penalty": 1.2,
		"frequency_penalty": 0.8,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.EqualValues(t, 1.2, raw["presence_penalty"])
	require.EqualValues(t, 0.8, raw["frequency_penalty"])
}

func TestNormalizeChatRequestPreservesStandardOpenAIAndAnthropicTopLevelFields(t *testing.T) {
	body, _, err := normalizeChatRequest([]byte(`{
		"messages": [{"role": "user", "content": "hello"}],
		"metadata": {"trace_id": "abc123"},
		"service_tier": "auto",
		"top_k": 40,
		"stop_sequences": ["\n\nHuman:"],
		"system": "use concise answers",
		"thinking": {"type": "enabled"},
		"tool_choice": "auto",
		"tools": [{"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}],
		"stream_options": {"include_usage": true},
		"response_format": {"type": "json_schema", "json_schema": {"name": "payload", "schema": {"type": "object"}}},
		"prompt_cache_key": "cache-key-1",
		"inference_geo": "us"
	}`))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, "abc123", raw["metadata"].(map[string]any)["trace_id"])
	require.Equal(t, "auto", raw["service_tier"])
	require.EqualValues(t, 40, raw["top_k"])
	require.Equal(t, "use concise answers", raw["system"])
	require.Equal(t, "enabled", raw["thinking"].(map[string]any)["type"])
	require.Equal(t, "auto", raw["tool_choice"])
	require.Len(t, raw["tools"].([]any), 1)
	require.Equal(t, true, raw["stream_options"].(map[string]any)["include_usage"])
	require.Equal(t, "json_schema", raw["response_format"].(map[string]any)["type"])
	require.Equal(t, "cache-key-1", raw["prompt_cache_key"])
	require.Equal(t, "us", raw["inference_geo"])
	require.Equal(t, "\n\nHuman:", raw["stop_sequences"].([]any)[0])
}

func TestNormalizeChatRequestRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "enforced tokens",
			body: `{"enforced_tokens":["x"],"messages":[{"role":"user","content":"hello"}]}`,
			want: "enforced_tokens",
		},
		{
			name: "json object response format",
			body: `{"response_format":{"type":"json_object"},"messages":[{"role":"user","content":"hello"}]}`,
			want: "json_object",
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
		{
			name: "text with extra field",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hello","image_url":{"url":"https://example.com/image.png"}}]}]}`,
			want: `must only include type and text`,
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
