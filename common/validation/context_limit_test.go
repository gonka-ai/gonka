package validation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var tokenizeJSONHeader = http.Header{"Content-Type": []string{"application/json"}}

func TestContextLimitCheck(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":4096,"temperature":0.7,"reasoning_effort":"high","chat_template_kwargs":{"x":1}}`)
	for _, tc := range []struct {
		name  string
		limit uint64
		code  int
		body  string
		want  ContextLimitVerdict
	}{
		{name: "over the limit", limit: 400000, code: 200, body: `{"count":397000,"max_model_len":1000000,"tokens":[]}`, want: ContextLimitExceeded},
		{name: "exactly at the limit fits", limit: 400000, code: 200, body: `{"count":395904}`, want: ContextLimitFits},
		{name: "fitting prompt", limit: 400000, code: 200, body: `{"count":1000}`, want: ContextLimitFits},
		{name: "unknown limit", code: 200, body: `{"count":397000}`, want: ContextLimitUncounted},
		{name: "failed tokenize", limit: 400000, code: 404, body: `not found`, want: ContextLimitUncounted},
		{name: "tokenize without count", limit: 400000, code: 200, body: `{"tokens":[]}`, want: ContextLimitUncounted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenize := func(_ context.Context, body []byte) (*http.Response, error) {
				var request map[string]any
				require.NoError(t, json.Unmarshal(body, &request))
				require.Equal(t, "m", request["model"])
				require.Equal(t, "hi", request["messages"].([]any)[0].(map[string]any)["content"])
				for _, dropped := range []string{"stream", "max_tokens", "temperature", "reasoning_effort"} {
					require.NotContains(t, request, dropped)
				}
				require.Equal(t, map[string]any{"x": float64(1), "reasoning_effort": "high", "enable_thinking": true}, request["chat_template_kwargs"])
				return &http.Response{StatusCode: tc.code, Header: tokenizeJSONHeader, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}

			got := ContextLimitCheck{Limit: tc.limit, Tokenize: tokenize}.Check(context.Background(), "1", prompt)

			require.Equal(t, tc.want, got)
		})
	}
	require.Equal(t, ContextLimitUncounted, ContextLimitCheck{Limit: 400000}.Check(context.Background(), "1", prompt))
}

func TestTokenizePrompt_RenamesReasoningContentLikeTheChatPath(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","content":"","reasoning_content":"think","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"assistant","content":"a","reasoning":"kept","reasoning_content":"dropped"},{"role":"tool","tool_call_id":"c","content":"r"}]}`)
	tokenize := func(_ context.Context, body []byte) (*http.Response, error) {
		var request map[string]any
		require.NoError(t, json.Unmarshal(body, &request))
		messages := request["messages"].([]any)
		first := messages[1].(map[string]any)
		require.Equal(t, "think", first["reasoning"])
		require.NotContains(t, first, "reasoning_content")
		second := messages[2].(map[string]any)
		require.Equal(t, "kept", second["reasoning"])
		require.NotContains(t, second, "reasoning_content")
		return &http.Response{StatusCode: 200, Header: tokenizeJSONHeader, Body: io.NopCloser(strings.NewReader(`{"count":42}`))}, nil
	}

	count, err := tokenizePrompt(context.Background(), prompt, tokenize)

	require.NoError(t, err)
	require.EqualValues(t, 42, count)
}
