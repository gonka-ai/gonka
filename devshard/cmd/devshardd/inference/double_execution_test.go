package inference

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"common/storage/payloads"
	devshardpkg "devshard"
)

// memoryPayloads keeps the first payload per epoch, as the Postgres store does.
type memoryPayloads struct {
	mu   sync.Mutex
	rows map[uint64][2][]byte
}

func (m *memoryPayloads) Store(_ context.Context, _ string, _, epochID uint64, prompt, response []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[uint64][2][]byte{}
	}
	if _, exists := m.rows[epochID]; exists {
		return payloads.ErrAlreadyStored
	}
	m.rows[epochID] = [2][]byte{prompt, response}
	return nil
}

func (m *memoryPayloads) Retrieve(_ context.Context, _ string, _, epochID uint64) ([]byte, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[epochID]
	if !ok {
		return nil, nil, payloads.ErrNotFound
	}
	return row[0], row[1], nil
}

const doubleExecutionPrompt = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func doubleExecutionRequest(t *testing.T, epoch uint64) devshardpkg.ExecuteRequest {
	t.Helper()
	canonical, err := devshardpkg.CanonicalizeJSON([]byte(doubleExecutionPrompt))
	if err != nil {
		t.Fatal(err)
	}
	promptHash := sha256.Sum256(canonical)
	return devshardpkg.ExecuteRequest{
		InferenceID: 1, EscrowID: "e", Model: "m",
		Prompt: []byte(doubleExecutionPrompt), PromptHash: promptHash[:],
		EpochID: epoch,
	}
}

// gatedModel serves one SSE completion saying content, with logprobs so the
// gateway view differs from the stored bytes, but only after release is closed.
func gatedModel(t *testing.T, content string, release <-chan struct{}) mlRequestExecutor {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"` + content + `"},"logprobs":{"content":[{"token":"` + content + `","logprob":-0.25,"top_logprobs":[]}]},"finish_reason":null}]}`,
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

// A host closed mid-generation (evictSession, ReloadStaleSession) leaves its
// detached execution running, and the reconnect on the new host signs a
// receipt for the still-pending inference by running the model again. When
// the detached run stores first, the second Store is ON CONFLICT DO NOTHING,
// yet the second result carried the hashes of its own bytes into
// MsgFinishInference, and validators check them against the stored payload.
func TestASecondExecutionCommitsTheHashesOfWhatIsStored(t *testing.T) {
	store := &memoryPayloads{}
	const epoch = 5
	req := doubleExecutionRequest(t, epoch)

	release := make(chan struct{})
	close(release)
	if _, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "first run", release), fixedChainParams{}, true); err != nil {
		t.Fatalf("first execution: %v", err)
	}
	second, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "second run", release), fixedChainParams{}, true)
	if err != nil {
		t.Fatalf("second execution: %v", err)
	}

	_, stored, err := store.Retrieve(context.Background(), "e", 1, epoch)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !strings.Contains(string(stored), "first run") {
		t.Fatalf("the store kept %q, want the first run", stored)
	}
	if err := verifyFetchedPayloadHashes(devshardpkg.ValidateRequest{
		InferenceID:  req.InferenceID,
		PromptHash:   req.PromptHash,
		ResponseHash: second.ResponseHash,
		ServedHash:   second.ServedHash,
	}, []byte(mustCanonical(t, doubleExecutionPrompt)), stored); err != nil {
		t.Fatalf("validators reject the second execution's finish against the stored payload: %v", err)
	}
	if string(second.ServedHash) == string(second.ResponseHash) {
		t.Fatal("the fixture should make the served view differ from the stored bytes")
	}
	if second.InputTokens != 7 || second.OutputTokens != 3 {
		t.Fatalf("usage = %d/%d, want the stored 7/3", second.InputTokens, second.OutputTokens)
	}
}

// The two runs overlap: the detached run is still in the model call when the
// reconnect starts its own, and finishes first.
func TestOverlappingExecutionsAgreeOnTheStoredPayload(t *testing.T) {
	store := &memoryPayloads{}
	const epoch = 5
	req := doubleExecutionRequest(t, epoch)

	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	results := make([]*devshardpkg.ExecuteResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "first run", firstRelease), fixedChainParams{}, true)
		if err != nil {
			t.Errorf("first execution: %v", err)
		}
		results[0] = r
		close(secondRelease)
	}()
	go func() {
		defer wg.Done()
		r, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "second run", secondRelease), fixedChainParams{}, true)
		if err != nil {
			t.Errorf("second execution: %v", err)
		}
		results[1] = r
	}()
	close(firstRelease)
	wg.Wait()
	if results[0] == nil || results[1] == nil {
		t.Fatal("an execution returned no result")
	}
	if string(results[0].ResponseHash) != string(results[1].ResponseHash) || string(results[0].ServedHash) != string(results[1].ServedHash) {
		t.Fatalf("the two finishes disagree: response %x vs %x, served %x vs %x",
			results[0].ResponseHash, results[1].ResponseHash, results[0].ServedHash, results[1].ServedHash)
	}
}

func mustCanonical(t *testing.T, prompt string) string {
	t.Helper()
	canonical, err := devshardpkg.CanonicalizeJSON([]byte(prompt))
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}

// Without PGHOST the payload store is FileStorage (DEVSHARD_STORAGE_MODE=auto
// resolves to sqlite). There the order was reversed from Postgres: a rename
// replaced the file, so the run that stored LAST was what validators fetch. If
// the reconnect's run finishes first and commits its hashes, the detached run
// on the closed host stores afterwards and must not replace those bytes.
func TestALateDetachedRunDoesNotReplaceTheCommittedFilePayload(t *testing.T) {
	store := payloads.NewFileStorage(t.TempDir())
	const epoch = 5
	req := doubleExecutionRequest(t, epoch)

	release := make(chan struct{})
	close(release)
	second, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "second run", release), fixedChainParams{}, true)
	if err != nil {
		t.Fatalf("reconnect execution: %v", err)
	}
	// The closed host's run comes back after the reconnect committed.
	if _, err := executeInference(context.Background(), req, store, epoch, gatedModel(t, "first run", release), fixedChainParams{}, true); err != nil {
		t.Fatalf("detached execution: %v", err)
	}

	_, stored, err := store.Retrieve(context.Background(), "e", 1, epoch)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if err := verifyFetchedPayloadHashes(devshardpkg.ValidateRequest{
		InferenceID:  req.InferenceID,
		PromptHash:   req.PromptHash,
		ResponseHash: second.ResponseHash,
		ServedHash:   second.ServedHash,
	}, []byte(mustCanonical(t, doubleExecutionPrompt)), stored); err != nil {
		t.Fatalf("validators reject the committed finish against the stored payload (stored: %q): %v", stored, err)
	}
}
