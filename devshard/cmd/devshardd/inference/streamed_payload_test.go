package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/completionapi"
	"common/validation"
	devshardpkg "devshard"
)

// The three shapes production sent at once: a prompt-sized first chunk, logprobs, and a stop token id.
func TestAStreamShapedLikeProductionIsStoredHashedAndParseable(t *testing.T) {
	promptTokenIDs := strings.TrimPrefix(strings.Repeat(",163586", 20_000), ",")
	chunks := []string{
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"moonshotai/Kimi-K2.6",` +
			`"prompt_token_ids":[` + promptTokenIDs + `],"choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"moonshotai/Kimi-K2.6",` +
			`"choices":[{"index":0,"delta":{"content":"Hi"},"token_ids":[258],"finish_reason":null,` +
			`"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"bytes":[72,105],"top_logprobs":[` +
			`{"token":"Hi","logprob":-0.5,"bytes":[72,105]},{"token":"9","logprob":-9999,"bytes":[57]}]}]}}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"moonshotai/Kimi-K2.6",` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop","stop_reason":163586,"token_ids":[163586]}],` +
			`"usage":{"prompt_tokens":20000,"completion_tokens":1}}`,
		"data: [DONE]",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk + "\n\n"))
		}
	}))
	defer server.Close()

	if len(chunks[0]) <= 64*1024 {
		t.Fatalf("the first chunk must exceed the scanner default: %d bytes", len(chunks[0]))
	}

	store := &recordingPayloadStore{}
	toGateway := httptest.NewRecorder()
	result, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{
			InferenceID: 1, EscrowID: "60453", Model: "moonshotai/Kimi-K2.6",
			Prompt:         []byte(`{"model":"moonshotai/Kimi-K2.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			ResponseWriter: toGateway,
		},
		store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{})
	if err != nil {
		t.Fatalf("executeInference: %v", err)
	}

	hash := sha256.Sum256(store.responsePayload)
	if string(result.ResponseHash) != string(hash[:]) {
		t.Fatal("the committed hash does not cover the stored bytes")
	}
	if result.InputTokens != 20000 || result.OutputTokens != 1 {
		t.Fatalf("usage was not read from the stream: in=%d out=%d", result.InputTokens, result.OutputTokens)
	}

	forwarded := toGateway.Body.String()
	for _, internal := range []string{`"token_ids"`, `"prompt_token_ids"`} {
		if strings.Contains(forwarded, internal) {
			t.Fatalf("%s reached the gateway", internal)
		}
		if strings.Contains(string(store.responsePayload), internal) {
			t.Fatalf("%s survived into the stored payload", internal)
		}
	}
	if !strings.Contains(forwarded, `"content":"Hi"`) {
		t.Fatalf("the answer did not reach the gateway: %s", forwarded)
	}

	if err := validation.VerifyPayloadHashes(nil, store.responsePayload, "", hex.EncodeToString(result.ResponseHash), "devshard-60453-1"); err != nil {
		t.Fatalf("the validator recomputes a different hash than the host committed: %v", err)
	}

	response, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(store.responsePayload)
	if err != nil {
		t.Fatalf("a validator cannot parse the stored payload: %v", err)
	}
	usage, err := response.GetUsage()
	if err != nil {
		t.Fatalf("a validator cannot read usage: %v", err)
	}
	if usage.PromptTokens != 20000 || usage.CompletionTokens != 1 {
		t.Fatalf("the validator reads a different usage: %+v", usage)
	}
	if _, err := response.GetEnforcedTokens(); err != nil {
		t.Fatalf("a validator cannot rebuild the enforced tokens: %v", err)
	}
}

// The empty-processor error is what production saw when the scanner died on the first line, so the
// shapes that can still produce it must fail the inference rather than store anything.
func TestAnAnswerlessStreamFailsTheInference(t *testing.T) {
	for name, testCase := range map[string]struct {
		chunks    []string
		wantError string
	}{
		"nothing at all": {wantError: "both jsonResponseBytes and streamedResponse are empty"},
		"only done":      {chunks: []string{"data: [DONE]"}, wantError: "no data available in streamed response"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, chunk := range testCase.chunks {
					_, _ = w.Write([]byte(chunk + "\n\n"))
				}
			}))
			defer server.Close()

			store := &recordingPayloadStore{}
			_, err := executeInference(context.Background(),
				devshardpkg.ExecuteRequest{
					InferenceID: 1, EscrowID: "60453", Model: "m",
					Prompt:         []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
					ResponseWriter: httptest.NewRecorder(),
				},
				store, 1,
				func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
					request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
					return http.DefaultClient.Do(request)
				},
				fixedChainParams{})

			if err == nil {
				t.Fatal("an answerless stream was accepted as a finished inference")
			}
			if !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("the failure does not name its cause: %v", err)
			}
			if store.responsePayload != nil {
				t.Fatalf("an answerless stream was stored: %s", store.responsePayload)
			}
		})
	}
}
