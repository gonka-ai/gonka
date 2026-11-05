package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

// Exercise the outgoing executor body, the CPU voter's reconstruction, and the
// existing replay path. A helper-only test would miss a caller that bypasses it.
func TestReasoningOutputExecutionAndValidation(t *testing.T) {
	for _, inclusion := range []string{"absent", "false", "true"} {
		for _, policy := range []struct {
			name     string
			effort   string
			thinking *bool
		}{
			{name: "default"},
			{name: "high", effort: "high", thinking: boolPointer(true)},
			{name: "none", effort: "none", thinking: boolPointer(false)},
		} {
			t.Run(inclusion+"/"+policy.name, func(t *testing.T) {
				fields := map[string]any{
					"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
					"max_tokens": 16, "stream": true,
				}
				if inclusion != "absent" {
					fields["include_reasoning"] = inclusion == "true"
				}
				if policy.effort != "" {
					fields["reasoning_effort"] = policy.effort
				}
				if policy.thinking != nil {
					fields["chat_template_kwargs"] = map[string]any{"enable_thinking": *policy.thinking}
				}
				prompt, err := json.Marshal(fields)
				require.NoError(t, err)
				original := append([]byte(nil), prompt...)
				store := &memoryPayloads{}
				var outgoing []byte
				_, err = executeInference(context.Background(), devshardpkg.ExecuteRequest{
					InferenceID: 42, EscrowID: "e", Model: "m", Prompt: prompt,
				}, store, 10, func(_ context.Context, _ string, body []byte) (*http.Response, error) {
					outgoing = append([]byte(nil), body...)
					return &http.Response{StatusCode: http.StatusOK,
						Header: http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:   io.NopCloser(strings.NewReader(strings.Join(answeredChunks, "\n\n") + "\n\n"))}, nil
				}, fixedChainParams{}, true, fixedVocabularySize(0))
				require.NoError(t, err)
				storedPrompt, _, err := store.Retrieve(context.Background(), "e", 42, 10)
				require.NoError(t, err)
				require.Equal(t, original, storedPrompt, "storage must retain the original signed prompt")
				require.Equal(t, original, prompt, "normalization must not mutate the caller's bytes")

				assertBody := func(label string, body []byte) {
					t.Helper()
					var got map[string]any
					require.NoError(t, json.Unmarshal(body, &got), label)
					assert.Equal(t, true, got["include_reasoning"], label+" must expose generated reasoning")
					for _, key := range []string{"reasoning_effort", "chat_template_kwargs"} {
						assert.Equal(t, fields[key], got[key], label+" must preserve "+key)
					}
				}
				assertBody("executor", outgoing)
				response := []byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"logprobs":{"content":[{"token":"42","logprob":-0.5,"top_logprobs":[{"token":"42","logprob":-0.5}]}]}}]}`)
				var replayBody []byte
				validator := newFaultTestValidator(10, true,
					func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
						return storedPrompt, response, nil
					}, func(_ context.Context, _, _ string, body []byte) (*http.Response, error) {
						replayBody = append([]byte(nil), body...)
						return &http.Response{StatusCode: http.StatusBadRequest, Body: http.NoBody}, nil
					}, nil)
				validator.vocabularySizes = stubVocabularyResolver{vocabularySize: 43}
				request := faultReq(10)
				request.InferenceID = 42
				fetched := validator.FetchChallengePayload(context.Background(), request)
				require.Equal(t, devshardpkg.ChallengeFetchFetched, fetched.Outcome)
				assertBody("CPU voter", fetched.Request)
				assert.Equal(t, outgoing, fetched.Request, "voters must reconstruct the exact outgoing request")
				_, err = validator.Validate(context.Background(), request)
				require.NoError(t, err)
				require.NotEmpty(t, replayBody, "existing replay must reach the ML request boundary")
				assertBody("replay", replayBody)
				require.Equal(t, original, storedPrompt)
			})
		}
	}
}

func boolPointer(value bool) *bool { return &value }
