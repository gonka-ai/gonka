package completionapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func streamedErrorPayload(t *testing.T, events ...string) []byte {
	t.Helper()
	payload, err := json.Marshal(SerializedStreamedResponse{Events: events})
	require.NoError(t, err)
	return payload
}

func TestModifyRequestBodyForVocabulary_BoundsTokenIDs(t *testing.T) {
	for _, tc := range []struct {
		name           string
		fields         string
		vocabularySize int
		want           map[string]any
		absent         []string
	}{
		{
			name:           "logit bias keys outside the vocabulary",
			fields:         `"logit_bias": {"5": 10, "999": -5, "1000": 100, "-1": 1, "99999999999999999999": 1, "abc": 2}`,
			vocabularySize: 1000,
			want:           map[string]any{"logit_bias": map[string]any{"5": float64(10), "999": float64(-5), "abc": float64(2)}},
		},
		{
			name:   "unknown vocabulary uses the coarse limit",
			fields: `"logit_bias": {"300000": 1, "9999999": 1}`,
			want:   map[string]any{"logit_bias": map[string]any{"300000": float64(1)}},
		},
		{
			name:   "logit bias with no key left",
			fields: `"logit_bias": {"999999999": 100}`,
			absent: []string{"logit_bias"},
		},
		{
			name:           "allowed token ids outside the vocabulary",
			fields:         `"allowed_token_ids": [1, 1000, -3, 999, 2.5, "x"]`,
			vocabularySize: 1000,
			want:           map[string]any{"allowed_token_ids": []any{float64(1), float64(999), 2.5, "x"}},
		},
		{
			name:           "allowed token ids with no id left stay empty",
			fields:         `"allowed_token_ids": [1000, 2000]`,
			vocabularySize: 1000,
			want:           map[string]any{"allowed_token_ids": []any{}},
		},
		{
			name:           "caller enforced tokens",
			fields:         `"enforced_tokens": {"tokens": [{"token": "999999999", "top_tokens": []}]}`,
			vocabularySize: 1000,
			absent:         []string{"enforced_tokens"},
		},
		{
			name:           "malformed fields are left to the engine",
			fields:         `"logit_bias": "not-a-map", "allowed_token_ids": "not-a-list"`,
			vocabularySize: 1000,
			want:           map[string]any{"logit_bias": "not-a-map", "allowed_token_ids": "not-a-list"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modified, err := ModifyRequestBodyForVocabulary([]byte(`{"messages": [{"role": "user", "content": "hi"}], `+tc.fields+`}`), 1, "", tc.vocabularySize)
			require.NoError(t, err)
			var request map[string]any
			require.NoError(t, json.Unmarshal(modified.NewBody, &request))
			for field, value := range tc.want {
				require.Equal(t, value, request[field], field)
			}
			for _, field := range tc.absent {
				require.NotContains(t, request, field)
			}
		})
	}
}

func TestTokenIDKeyOutOfRange_PythonIntegerForms(t *testing.T) {
	for _, key := range []string{"1_000", "１０００", "١٠٠٠", "𝟙𝟘𝟘𝟘", " +001000 ", "-١", "99999999999999999999", "9_999_999_999_999_999_999_999"} {
		require.True(t, TokenIDKeyOutOfRange(key, 1000), key)
	}
	for _, key := range []string{"9_99", "９９９", "١٢", "+0007", "-0", "abc", "1.5", "1e3", "_1000", "1000_", "1__000", "+_1000", "²", ""} {
		require.False(t, TokenIDKeyOutOfRange(key, 1000), key)
	}
}

func TestIsClientFaultErrorResponse(t *testing.T) {
	clientFault := `data: {"error":{"code":400,"message":"bad","type":"BadRequestError"}}`
	for _, tc := range []struct {
		name   string
		events []string
		want   bool
	}{
		{name: "bad request", events: []string{clientFault, `data: [DONE]`}, want: true},
		{name: "unprocessable entity", events: []string{`data: {"error":{"code":422,"message":"bad"}}`}, want: true},
		{name: "legacy error object", events: []string{`data: {"object":"error","message":"bad","type":"BadRequestError","code":400}`}, want: true},
		{name: "role chunk first", events: []string{`data: {"choices":[{"delta":{"role":"assistant","content":null,"reasoning":null,"tool_calls":null,"function_call":null,"refusal":null},"logprobs":null}],"usage":null}`, clientFault}, want: true},
		{name: "engine failure", events: []string{`data: {"error":{"code":500,"message":"boom"}}`}},
		{name: "rate limited", events: []string{`data: {"error":{"code":429,"message":"busy"}}`}},
		{name: "engine failure next to a client fault", events: []string{clientFault, `data: {"error":{"code":500}}`}},
		{name: "unparseable data", events: []string{`data: {not json`, clientFault}},
		{name: "unparseable data after the error", events: []string{clientFault, `data: {not json`}},
		{name: "completion", events: []string{`data: {"choices":[{"delta":{"content":"hi"}}]}`, `data: [DONE]`}},
		{name: "output in the error chunk", events: []string{`data: {"error":{"code":400},"choices":[{"delta":{"content":"answer"}}]}`}},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload []byte
			if tc.events != nil {
				payload = streamedErrorPayload(t, tc.events...)
			}
			require.Equal(t, tc.want, IsClientFaultErrorResponse(payload))
		})
	}
}

func TestIsClientFaultErrorResponse_AnyOutputDisqualifies(t *testing.T) {
	clientFault := `data: {"error":{"code":400,"message":"bad"}}`
	for _, output := range []string{
		`data: {"choices":[{"delta":{"content":"answer"}}]}`,
		`data: {"choices":[{"delta":{"content":[{"text":"answer"}]}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`,
		`data: {"choices":[{"delta":{"function_call":{"name":"f","arguments":"{}"}}}]}`,
		`data: {"choices":[{"delta":{"refusal":"No"}}]}`,
		`data: {"choices":[{"message":{"refusal":"No"}}]}`,
		`data: {"choices":[{"text":"answer"}]}`,
		`data: {"choices":[{"delta":{},"logprobs":{"content":[{"token":"42"}]}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	} {
		require.False(t, IsClientFaultErrorResponse(streamedErrorPayload(t, output, clientFault)), output)
		require.False(t, IsClientFaultErrorResponse(streamedErrorPayload(t, clientFault, output)), output)
	}
}

func TestCompactClientFaultBody(t *testing.T) {
	pad := strings.Repeat("x", 20<<10)
	for name, body := range map[string]string{
		"error object":        `{"error":{"code":400,"message":"` + pad + `","type":"BadRequestError"},"id":"x"}`,
		"string code":         `{"error":{"code":"422","message":"` + pad + `"}}`,
		"legacy object error": `{"object":"error","code":400,"message":"` + pad + `"}`,
		"empty choices":       `{"error":{"code":400,"message":"` + pad + `"},"choices":[]}`,
		"empty usage":         `{"error":{"code":400,"message":"` + pad + `"},"usage":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			compact := CompactClientFaultBody([]byte(body))
			require.LessOrEqual(t, len(compact), MaxClientFaultBodyBytes)
			require.True(t, IsClientFaultErrorResponse(streamedErrorPayload(t, DataPrefix+string(compact))))
		})
	}
	for name, body := range map[string]string{
		"small":            `{"error":{"code":400,"message":"bad"}}`,
		"server fault":     `{"error":{"code":500,"message":"` + pad + `"}}`,
		"output evidence":  `{"error":{"code":400,"message":"` + pad + `"},"choices":[{"delta":{"content":"answer"}}]}`,
		"billed usage":     `{"error":{"code":400,"message":"` + pad + `"},"usage":{"completion_tokens":1}}`,
		"string error":     `{"error":"` + pad + `"}`,
		"numeric message":  `{"error":{"code":400,"message":5},"pad":"` + pad + `"}`,
		"ordinary content": `{"choices":[{"delta":{"content":"` + pad + `"}}]}`,
		"not json":         `{"error":` + pad,
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, body, string(CompactClientFaultBody([]byte(body))))
		})
	}
}
