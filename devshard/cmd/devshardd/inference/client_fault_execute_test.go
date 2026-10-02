package inference

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"
	commonvalidation "common/validation"
	devshardpkg "devshard"
)

func executeAgainst(t *testing.T, handler http.HandlerFunc, prompt string, vocabularySize int) (*devshardpkg.ExecuteResult, *recordingPayloadStore, *httptest.ResponseRecorder, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := &recordingPayloadStore{}
	toGateway := httptest.NewRecorder()
	result, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{InferenceID: 1, EscrowID: "1", Model: "m", Prompt: []byte(prompt), ResponseWriter: toGateway},
		store, 1,
		func(ctx context.Context, _ string, body []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(body)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{}, true, vocabularySize)
	return result, store, toGateway, err
}

func jsonErrorHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestExecuteInferenceSignsAZeroTokenFinishForAClientFaultRejection(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"json bad request":         jsonErrorHandler(http.StatusBadRequest, `{"error":{"message":"truncate_prompt_tokens must be >= -1","type":"BadRequestError","param":null,"code":400}}`),
		"json unprocessable":       jsonErrorHandler(http.StatusUnprocessableEntity, `{"error":{"message":"bad field","type":"BadRequestError","code":422}}`),
		"pretty legacy error body": jsonErrorHandler(http.StatusBadRequest, "{\n \"object\":\"error\",\n \"code\":400,\n \"message\":\"bad\"\n}"),
	} {
		t.Run(name, func(t *testing.T) {
			result, store, toGateway, err := executeAgainst(t, handler, streamingPrompt, 0)

			require.NoError(t, err)
			require.Zero(t, result.InputTokens)
			require.Zero(t, result.OutputTokens)
			require.True(t, completionapi.IsClientFaultErrorResponse(store.responsePayload))
			require.Equal(t, sha256.Sum256(store.responsePayload), [32]byte(result.ResponseHash))
			require.Contains(t, receivedSums(toGateway.Body.String()), [32]byte(result.ServedHash))
			require.True(t, strings.HasSuffix(toGateway.Body.String(), "data: [DONE]\n\n"))
		})
	}
}

func TestExecuteInferenceFailsWithoutAClientFaultBody(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"engine error":              jsonErrorHandler(http.StatusInternalServerError, `{"error":{"message":"EngineCore encountered an issue","type":"InternalServerError","code":500}}`),
		"bad request without json":  jsonErrorHandler(http.StatusBadRequest, `not json`),
		"bad request without error": jsonErrorHandler(http.StatusBadRequest, `{"detail":"bad"}`),
	} {
		t.Run(name, func(t *testing.T) {
			result, store, _, err := executeAgainst(t, handler, streamingPrompt, 0)

			require.Error(t, err)
			require.Nil(t, result)
			require.Nil(t, store.responsePayload)
		})
	}
}

func TestExecuteInferenceSendsOnlyInVocabTokenIDsToTheEngine(t *testing.T) {
	for _, tc := range []struct {
		name           string
		logitBias      string
		vocabularySize int
		want           any
	}{
		{name: "resolved vocabulary", logitBias: `{"300000":5,"7":1}`, vocabularySize: 200064, want: map[string]any{"7": float64(1)}},
		{name: "unknown vocabulary drops the reported key", logitBias: `{"999999999":100}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]any
			handler := func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &sent)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1}}`+"\n\ndata: [DONE]\n\n")
			}
			prompt := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"logit_bias":` + tc.logitBias + `,"enforced_tokens":{"tokens":[]}}`

			_, _, _, err := executeAgainst(t, handler, prompt, tc.vocabularySize)

			require.NoError(t, err)
			require.Equal(t, tc.want, sent["logit_bias"])
			require.NotContains(t, sent, "enforced_tokens")
		})
	}
}

func TestExecuteInferenceLargeClientFaultFitsZeroTokenPayloadLimit(t *testing.T) {
	prompt := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"temperature":"` + strings.Repeat("x", 6<<20) + `"}`
	response := `{"error":{"code":400,"message":"` + strings.Repeat("x", 6<<20) + `","type":"BadRequestError"}}`
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "json", handler: jsonErrorHandler(http.StatusBadRequest, response)},
		{name: "sse", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+response+"\n\ndata: [DONE]\n\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, store, _, err := executeAgainst(t, tc.handler, prompt, 0)
			require.NoError(t, err)
			require.Zero(t, result.InputTokens)
			require.Zero(t, result.OutputTokens)
			require.True(t, completionapi.IsClientFaultErrorResponse(store.responsePayload))
			canonicalPrompt, err := devshardpkg.CanonicalizeJSON([]byte(prompt))
			require.NoError(t, err)
			wire, err := json.Marshal(commonvalidation.PayloadResponse{
				InferenceId:       "devshard-1-1",
				PromptPayload:     canonicalPrompt,
				ResponsePayload:   store.responsePayload,
				ExecutorSignature: strings.Repeat("s", 256),
			})
			require.NoError(t, err)
			require.LessOrEqual(t, int64(len(wire)), commonvalidation.PayloadResponseByteLimit(0))
		})
	}
}

func TestExecuteInferencePassesThroughLargeChunksThatDoNotDecodeAsErrors(t *testing.T) {
	pad := strings.Repeat("x", 17<<10)
	for name, chunk := range map[string]string{
		"string error":         `{"error":"` + pad + `"}`,
		"numeric message":      `{"object":"error","message":5,"pad":"` + pad + `"}`,
		"object message":       `{"error":{"code":400,"message":{"a":"` + pad + `"}}}`,
		"ordinary large chunk": `{"choices":[{"delta":{"content":"` + pad + `"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			handler := func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
			}
			_, store, _, err := executeAgainst(t, handler, streamingPrompt, 0)
			require.NoError(t, err)
			require.False(t, completionapi.IsClientFaultErrorResponse(store.responsePayload))
			require.Contains(t, string(store.responsePayload), pad)
		})
	}
}

func TestExecuteInferenceStreamingClientFaultDoesNotBillPromptUsage(t *testing.T) {
	for _, tc := range []struct {
		name, first           string
		wantInput, wantOutput uint64
	}{
		{name: "role with prompt usage", first: `data: {"choices":[{"delta":{"role":"assistant"}}],"usage":{"prompt_tokens":7,"completion_tokens":0}}`},
		{name: "real output remains billed", first: `data: {"choices":[{"delta":{"content":"answer"}}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`, wantInput: 7, wantOutput: 1},
		{name: "hidden output remains billed", first: `data: {"choices":[{"delta":{},"logprobs":{"content":[{"token":"42"}]}}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`, wantInput: 7, wantOutput: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.first+"\n\ndata: {\"error\":{\"code\":400,\"message\":\"bad\"}}\n\ndata: [DONE]\n\n")
			}
			result, store, gateway, err := executeAgainst(t, handler, streamingPrompt, 0)
			require.NoError(t, err)
			require.Equal(t, tc.wantInput, result.InputTokens)
			require.Equal(t, tc.wantOutput, result.OutputTokens)
			require.Contains(t, receivedSums(gateway.Body.String()), [32]byte(result.ServedHash))
			projected, err := completionapi.StripForGateway(store.responsePayload)
			require.NoError(t, err)
			require.Equal(t, completionapi.IsClientFaultErrorResponse(store.responsePayload), completionapi.IsClientFaultErrorResponse(projected))
		})
	}
}
