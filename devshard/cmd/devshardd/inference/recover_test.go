package inference

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/storage/payloads"
	devshardpkg "devshard"
)

type memoryPayloads struct {
	rows  map[uint64][2][]byte
	reads []uint64
	err   error
}

func (m *memoryPayloads) Store(_ context.Context, _ string, _, epochID uint64, prompt, response []byte) error {
	if m.rows == nil {
		m.rows = map[uint64][2][]byte{}
	}
	if _, exists := m.rows[epochID]; !exists {
		m.rows[epochID] = [2][]byte{prompt, response}
	}
	return nil
}

func (m *memoryPayloads) Retrieve(_ context.Context, _ string, _, epochID uint64) ([]byte, []byte, error) {
	m.reads = append(m.reads, epochID)
	if m.err != nil {
		return nil, nil, m.err
	}
	row, ok := m.rows[epochID]
	if !ok {
		return nil, nil, payloads.ErrNotFound
	}
	return row[0], row[1], nil
}

const recoverPrompt = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func streamedExecution(t *testing.T, store PayloadStore, epoch uint64) *devshardpkg.ExecuteResult {
	t.Helper()
	chunks := []string{
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		"data: [DONE]",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk + "\n\n"))
		}
	}))
	defer server.Close()
	result, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{InferenceID: 1, EscrowID: "e", Model: "m", Prompt: []byte(recoverPrompt)},
		store, epoch,
		func(ctx context.Context, _ string, body []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(body)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{}, true)
	if err != nil {
		t.Fatalf("executeInference: %v", err)
	}
	return result
}

func recoveryRequest(t *testing.T, mode devshardpkg.Recovery, escrowEpoch uint64) devshardpkg.ExecuteRequest {
	t.Helper()
	canonical, err := devshardpkg.CanonicalizeJSON([]byte(recoverPrompt))
	if err != nil {
		t.Fatal(err)
	}
	promptHash := sha256.Sum256(canonical)
	return devshardpkg.ExecuteRequest{
		InferenceID: 1, EscrowID: "e", Model: "m",
		Prompt: []byte(recoverPrompt), PromptHash: promptHash[:],
		EpochID: escrowEpoch, Recovery: mode,
	}
}

func TestRecoveryRebuildsTheResultTheFirstExecutionCommitted(t *testing.T) {
	store := &memoryPayloads{}
	original := streamedExecution(t, store, 6)

	req := recoveryRequest(t, devshardpkg.RecoveryStoredFirst, 5)
	writer := httptest.NewRecorder()
	req.ResponseWriter = writer
	ran := false
	result, err := executeWithRecovery(context.Background(), req, store, 6, func(context.Context) (*devshardpkg.ExecuteResult, error) {
		ran = true
		return nil, errors.New("the model must not run")
	})
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if ran {
		t.Fatal("a stored response must not run the model again")
	}
	if string(result.ResponseHash) != string(original.ResponseHash) {
		t.Fatal("the recovered hash differs from the one the first execution committed")
	}
	if result.InputTokens != original.InputTokens || result.OutputTokens != original.OutputTokens {
		t.Fatalf("recovered usage %d/%d, first execution %d/%d", result.InputTokens, result.OutputTokens, original.InputTokens, original.OutputTokens)
	}
	if got := store.reads; len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("epochs read %v, want [5 6]", got)
	}
	streamed := writer.Body.String()
	if !strings.Contains(streamed, `"content":"Hi"`) || !strings.HasSuffix(streamed, "data: [DONE]\n\n") {
		t.Fatalf("the stored response was not replayed as SSE: %q", streamed)
	}
	if strings.Count(streamed, "[DONE]") != 1 {
		t.Fatalf("replay must end with exactly one [DONE]: %q", streamed)
	}
}

func TestRecoveryRunsTheModelOnlyWhenNothingIsStored(t *testing.T) {
	store := &memoryPayloads{}
	want := &devshardpkg.ExecuteResult{ResponseHash: []byte("fresh")}

	got, err := executeWithRecovery(context.Background(), recoveryRequest(t, devshardpkg.RecoveryStoredFirst, 5), store, 5,
		func(context.Context) (*devshardpkg.ExecuteResult, error) { return want, nil })
	if err != nil || got != want {
		t.Fatalf("stored-first miss must run the model: %v %v", got, err)
	}

	_, err = executeWithRecovery(context.Background(), recoveryRequest(t, devshardpkg.RecoveryStoredOnly, 5), store, 5,
		func(context.Context) (*devshardpkg.ExecuteResult, error) {
			t.Fatal("stored-only must not run the model")
			return nil, nil
		})
	if !errors.Is(err, devshardpkg.ErrNoStoredResponse) {
		t.Fatalf("stored-only miss: %v", err)
	}

	store.reads = nil
	got, err = executeWithRecovery(context.Background(), recoveryRequest(t, devshardpkg.RecoveryNone, 5), store, 5,
		func(context.Context) (*devshardpkg.ExecuteResult, error) { return want, nil })
	if err != nil || got != want || len(store.reads) != 0 {
		t.Fatalf("no recovery must not read storage: reads=%v err=%v", store.reads, err)
	}
}

func TestRecoveryFailsClosed(t *testing.T) {
	failing := &memoryPayloads{err: errors.New("db down")}
	_, err := executeWithRecovery(context.Background(), recoveryRequest(t, devshardpkg.RecoveryStoredFirst, 5), failing, 5,
		func(context.Context) (*devshardpkg.ExecuteResult, error) {
			t.Fatal("a failed read must not run the model")
			return nil, nil
		})
	if err == nil {
		t.Fatal("a failed read must fail the execution")
	}

	store := &memoryPayloads{}
	streamedExecution(t, store, 5)
	req := recoveryRequest(t, devshardpkg.RecoveryStoredFirst, 5)
	req.PromptHash = []byte("another prompt")
	_, err = executeWithRecovery(context.Background(), req, store, 5,
		func(context.Context) (*devshardpkg.ExecuteResult, error) {
			t.Fatal("a stored row for another prompt must not run the model")
			return nil, nil
		})
	if err == nil {
		t.Fatal("a stored prompt that does not match the inference must fail")
	}
}

func TestRecoveryEpochs(t *testing.T) {
	for _, c := range []struct {
		escrow, phase uint64
		want          []uint64
	}{
		{5, 5, []uint64{5, 6, 4}},
		{5, 6, []uint64{5, 6, 4}},
		{5, 8, []uint64{5, 6, 8, 4}},
		{0, 0, []uint64{0, 1}},
	} {
		got := recoveryEpochs(c.escrow, c.phase)
		if len(got) != len(c.want) {
			t.Fatalf("recoveryEpochs(%d, %d) = %v, want %v", c.escrow, c.phase, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("recoveryEpochs(%d, %d) = %v, want %v", c.escrow, c.phase, got, c.want)
			}
		}
	}
}
