package inference

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	devshardpkg "devshard"
)

// gatedModel serves one SSE completion saying content, but only after release is closed.
func gatedModel(t *testing.T, content string, release <-chan struct{}) mlRequestExecutor {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"` + content + `"},"finish_reason":null}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
			"data: [DONE]",
		} {
			_, _ = w.Write([]byte(chunk + "\n\n"))
		}
	}))
	t.Cleanup(server.Close)
	return func(ctx context.Context, _ string, body []byte) (*http.Response, error) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(body)))
		return http.DefaultClient.Do(request)
	}
}

// A host closed mid-generation (evictSession/ReloadStaleSession) leaves its
// detached execution running. The reconnect on the new host finds nothing
// stored yet, so RecoveryStoredFirst runs the model a second time. The first
// run stores first; the second Store is ON CONFLICT DO NOTHING, yet its result
// still carries the hash of its own bytes. That hash goes into
// MsgFinishInference, and validators compare it with the stored payload.
func TestASecondExecutionCommitsTheHashOfWhatIsStored(t *testing.T) {
	store := &memoryPayloads{}
	const epoch = 5

	firstRelease := make(chan struct{})
	firstDone := make(chan *devshardpkg.ExecuteResult, 1)
	go func() {
		req := recoveryRequest(t, devshardpkg.RecoveryNone, epoch)
		result, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "first run", firstRelease), fixedChainParams{})
		if err != nil {
			t.Errorf("first execution: %v", err)
		}
		firstDone <- result
	}()

	secondRelease := make(chan struct{})
	secondDone := make(chan *devshardpkg.ExecuteResult, 1)
	req := recoveryRequest(t, devshardpkg.RecoveryStoredFirst, epoch)
	go func() {
		result, err := executeWithRecovery(context.Background(), req, store, epoch, func(ctx context.Context) (*devshardpkg.ExecuteResult, error) {
			// The recovery read has already missed: the first run has not stored yet.
			close(firstRelease)
			<-firstDone
			close(secondRelease)
			return executeInference(ctx, req, store, epoch, gatedModel(t, "second run", secondRelease), fixedChainParams{})
		})
		if err != nil {
			t.Errorf("second execution: %v", err)
		}
		secondDone <- result
	}()
	second := <-secondDone
	if second == nil {
		t.Fatal("second execution returned no result")
	}

	_, stored, err := store.Retrieve(context.Background(), "e", 1, epoch)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	storedHash := sha256.Sum256(stored)
	if string(second.ResponseHash) != string(storedHash[:]) {
		t.Fatalf("the finish would commit %x, but validators fetch a payload hashing to %x (stored: %q)",
			second.ResponseHash, storedHash[:], stored)
	}
}
