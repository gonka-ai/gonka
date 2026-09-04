package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	devshardpkg "devshard"
)

type recordingPayloadStore struct {
	responsePayload []byte
}

func (s *recordingPayloadStore) Store(_ context.Context, _ string, _, _ uint64, _, responsePayload []byte) error {
	s.responsePayload = responsePayload
	return nil
}

type fixedChainParams struct{}

func (fixedChainParams) LogprobsMode() string { return "" }

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
