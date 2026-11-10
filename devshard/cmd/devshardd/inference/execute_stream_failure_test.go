package inference

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	devshardpkg "devshard"
)

// A cut stream must fail: what is stored is hashed and paid for.
func TestExecuteInferenceRefusesATruncatedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m",` +
			`"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer server.Close()

	store := &recordingPayloadStore{}
	toGateway := httptest.NewRecorder()

	_, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{
			InferenceID: 1, EscrowID: "60453", Model: "m",
			Prompt:         []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			ResponseWriter: toGateway,
		},
		store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{})

	if err == nil {
		t.Fatal("a cut stream was accepted as a finished inference")
	}
	if store.responsePayload != nil {
		t.Fatalf("a truncated payload was stored: %s", store.responsePayload)
	}
}

// The caller leaving is not the host's fault: the answer is complete, so it is stored and committed.
func TestACallerThatStopsReadingStillFinishesTheInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m",` +
				`"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[],` +
				`"usage":{"prompt_tokens":7,"completion_tokens":1}}`,
			"data: [DONE]",
		} {
			_, _ = w.Write([]byte(chunk + "\n\n"))
		}
	}))
	defer server.Close()

	store := &recordingPayloadStore{}
	result, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{
			InferenceID: 1, EscrowID: "60453", Model: "m",
			Prompt:         []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			ResponseWriter: brokenPipeWriter{httptest.NewRecorder()},
		},
		store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{})

	if err != nil {
		t.Fatalf("a finished inference was thrown away because the caller left: %v", err)
	}
	if store.responsePayload == nil {
		t.Fatal("the answer the host produced was not stored")
	}
	if result.InputTokens != 7 || result.OutputTokens != 1 {
		t.Fatalf("usage was lost with the caller: in=%d out=%d", result.InputTokens, result.OutputTokens)
	}
}

// brokenPipeWriter is a caller that went away mid-stream.
type brokenPipeWriter struct{ http.ResponseWriter }

func (brokenPipeWriter) Write([]byte) (int, error) {
	return 0, &net.OpError{Op: "write", Err: net.ErrClosed}
}
