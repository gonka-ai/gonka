package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/types"
)

// Test flow:
//  1. Fetch a stored answer whose final call was cut inside a string argument.
//  2. Use the real render client against an HTTP endpoint that rejects invalid arguments.
//  3. Inspect the transmitted arguments and require the original request and response to stay intact.
func TestAVoterRepairsCutStringArgumentsForRender(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments string
		want      string
	}{
		{"whole object", ` { "s" : "already whole", "n": 7 } `, ` { "s" : "already whole", "n": 7 } `},
		{"whole null", `{"s":null}`, `{"s":null}`},
		{"trailing spaces", `{"s":"received   `, `{"s":"received   "}`},
		{"encoded tab and newline", `{"s":"received\t\n `, `{"s":"received\t\n "}`},
		{"empty string", `{"s":"`, `{"s":""}`},
		{"string prefix", `{"s":"devshard escrow`, `{"s":"devshard escrow"}`},
		{"saved mid-word cut", `{"query": "devshard esc`, `{"query": "devshard esc"}`},
		{"saved full-value cut", `{"query": "devshard escrow settlement`, `{"query": "devshard escrow settlement"}`},
		{"completed fields", `{"n":9007199254740993,"nested":{"k":[1,true,null]},"s":"received`, `{"n":9007199254740993,"nested":{"k":[1,true,null]},"s":"received"}`},
		{"escaped quote", `{"s":"say \"hello`, `{"s":"say \"hello"}`},
		{"escaped backslash", `{"s":"path\\`, `{"s":"path\\"}`},
		{"unfinished escape", `{"s":"path\`, `{"s":"path"}`},
		{"unfinished unicode", `{"s":"caf\u00`, `{"s":"caf"}`},
		{"unfinished unicode and escape", `{"s":"caf\u123\`, `{"s":"caf"}`},
		{"unfinished unicode text", `{"s":"caf\u東京`, `{"s":"caf"}`},
		{"unfinished long unicode", `{"s":"caf\U0000`, `{"s":"caf"}`},
		{"unfinished hex", `{"s":"caf\x0`, `{"s":"caf"}`},
		{"complete unicode", `{"s":"caf\u00e9`, `{"s":"caf\u00e9"}`},
		{"unicode pair", `{"s":"\ud83d\ude00`, `{"s":"\ud83d\ude00"}`},
		{"literal unicode escape", `{"s":"\\u00`, `{"s":"\\u00"}`},
		{"unicode text", `{"s":"café 東京`, `{"s":"café 東京"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetch, rec := argumentChallenge(t, tc.arguments)
			requestBefore, responseBefore := string(fetch.Request), string(fetch.Response)
			var mu sync.Mutex
			var sent []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions/render" {
					t.Errorf("unexpected render path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				var body struct {
					Messages []struct {
						ToolCalls []struct {
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode render body: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				n := 2
				for _, message := range body.Messages {
					for _, call := range message.ToolCalls {
						mu.Lock()
						sent = append(sent, call.Function.Arguments)
						mu.Unlock()
						if !wholeArguments(call.Function.Arguments) {
							http.Error(w, "invalid tool arguments", http.StatusBadRequest)
							return
						}
						n += 8
					}
				}
				// This is a transport fixture: only a valid transmitted object can be counted.
				_ = json.NewEncoder(w).Encode(map[string]any{"token_ids": make([]int, n)})
			}))
			defer server.Close()
			h := &Host{renderer: NewRenderClient(map[string][]string{rec.Model: {server.URL}}, nil)}
			verdict, count, reject, cause := h.fetchedVerdict(context.Background(), fetch, rec)
			require.Empty(t, reject, cause)
			require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, verdict.Fault)
			require.Empty(t, count.outputSkipped)
			require.EqualValues(t, 8, count.output)
			mu.Lock()
			got := append([]string(nil), sent...)
			mu.Unlock()
			require.Equal(t, []string{tc.want}, got)
			require.Equal(t, requestBefore, string(fetch.Request))
			require.Equal(t, responseBefore, string(fetch.Response))
			original, err := parseServedAnswer(fetch.Response)
			require.NoError(t, err)
			require.Equal(t, tc.arguments, original.calls[0].arguments)
		})
	}
}

// A malformed escape is not missing syntax the voter can close. It must not
// become an empty object, an inflated-usage vote, or a successful NONE vote.
func TestAVoterAbstainsWhenStringArgumentsCannotBeRepaired(t *testing.T) {
	for _, arguments := range []string{`{"s":"bad\q`, `{"s":"bad\u00zz`, `{"s":"bad\u\\u`, `{"s":{"nested":"cut`} {
		t.Run(arguments, func(t *testing.T) {
			fetch, rec := argumentChallenge(t, arguments)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("unrepairable arguments reached the renderer")
				http.Error(w, "unexpected request", http.StatusBadRequest)
			}))
			defer server.Close()
			h := &Host{renderer: NewRenderClient(map[string][]string{rec.Model: {server.URL}}, nil)}
			_, _, reject, cause := h.fetchedVerdict(context.Background(), fetch, rec)
			require.Equal(t, ChallengeRejectNoRender, reject)
			require.Contains(t, cause, "repair tool arguments")
		})
	}
}

func argumentChallenge(t *testing.T, arguments string) (devshard.ChallengeFetch, types.InferenceRecord) {
	t.Helper()
	const model = "MiniMaxAI/MiniMax-M2.7"
	request := []byte(`{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"s":{"type":["string","null"]},"query":{"type":"string"}}}}}]}`)
	positions := make([]any, 8)
	for i := range positions {
		positions[i] = map[string]any{"token": "x"}
	}
	response, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "finish_reason": "length",
		"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
			"id": "call_original", "type": "function", "function": map[string]any{"name": "f", "arguments": arguments},
		}}},
		"logprobs": map[string]any{"content": positions},
	}}})
	require.NoError(t, err)
	return devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Request: request, Response: response},
		types.InferenceRecord{Model: model, InputTokens: 1, OutputTokens: 8}
}
