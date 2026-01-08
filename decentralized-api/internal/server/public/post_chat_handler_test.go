package public

import (
	"context"
	"encoding/json"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type mockPayloadStorage struct {
	stored         map[string]struct{ prompt, response []byte }
	storeErr       error
	retrieveErr    error
	retrieveCalled bool
}

func newMockPayloadStorage() *mockPayloadStorage {
	return &mockPayloadStorage{
		stored: make(map[string]struct{ prompt, response []byte }),
	}
}

func (m *mockPayloadStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.stored[inferenceId] = struct{ prompt, response []byte }{promptPayload, responsePayload}
	return nil
}

func (m *mockPayloadStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	m.retrieveCalled = true
	if m.retrieveErr != nil {
		return nil, nil, m.retrieveErr
	}
	data, ok := m.stored[inferenceId]
	if !ok {
		return nil, nil, payloadstorage.ErrNotFound
	}
	return data.prompt, data.response, nil
}

func (m *mockPayloadStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	return nil
}

func newTestPhaseTracker(epochIndex uint64) *chainphase.ChainPhaseTracker {
	tracker := chainphase.NewChainPhaseTracker()
	epoch := types.Epoch{Index: epochIndex}
	params := types.EpochParams{
		EpochLength:      200,
		PocStageDuration: 50,
	}
	tracker.Update(
		chainphase.BlockInfo{Height: 100, Hash: "abc"},
		&epoch,
		&params,
		true,
		nil,
	)
	return tracker
}

func TestStorePayloadsToStorage_Success(t *testing.T) {
	storage := newMockPayloadStorage()
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":123,"messages":[{"role":"user","content":"hello"}]}`)
	responsePayload := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-1", promptPayload, responsePayload)

	require.Len(t, storage.stored, 1)
	stored := storage.stored["inf-1"]
	require.Equal(t, promptPayload, stored.prompt)
	require.Equal(t, responsePayload, stored.response)
}

func TestStorePayloadsToStorage_NilStorage(t *testing.T) {
	s := &Server{
		payloadStorage: nil,
		phaseTracker:   newTestPhaseTracker(5),
	}

	// Should not panic with nil storage
	s.storePayloadsToStorage(context.Background(), "inf-1", []byte("prompt"), []byte("response"))
}

func TestStorePayloadsToStorage_NilPhaseTracker(t *testing.T) {
	storage := newMockPayloadStorage()
	s := &Server{
		payloadStorage: storage,
		phaseTracker:   nil,
	}

	// Should not panic with nil phase tracker
	s.storePayloadsToStorage(context.Background(), "inf-1", []byte("prompt"), []byte("response"))
	require.Len(t, storage.stored, 0)
}

func TestStorePayloadsToStorage_Retrieval(t *testing.T) {
	storage := newMockPayloadStorage()
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":123}`)
	responsePayload := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-1", promptPayload, responsePayload)

	// Verify the stored payload can be retrieved
	storedPrompt, storedResponse, err := storage.Retrieve(context.Background(), "inf-1", 5)
	require.NoError(t, err)
	require.Equal(t, promptPayload, storedPrompt)
	require.Equal(t, responsePayload, storedResponse)
}

func TestFileStorageIntegration(t *testing.T) {
	dir := t.TempDir()
	storage := payloadstorage.NewFileStorage(dir)
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":42,"messages":[{"role":"user","content":"test"}]}`)
	responsePayload := []byte(`{"id":"inf-123","choices":[{"index":0,"message":{"role":"assistant","content":"response"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-123", promptPayload, responsePayload)

	storedPrompt, storedResponse, err := storage.Retrieve(context.Background(), "inf-123", 5)
	require.NoError(t, err)
	require.Equal(t, promptPayload, storedPrompt)
	require.Equal(t, responsePayload, storedResponse)
}

func TestEmptyButParseableResponsePayload_EnforcedTokensEmptySlice(t *testing.T) {
	resp := emptyButParseableResponsePayload("inf-empty", "test-model", 1)
	require.NotNil(t, resp)

	enforcedTokens, err := resp.GetEnforcedTokens()
	require.NoError(t, err)

	b, err := json.Marshal(enforcedTokens)
	require.NoError(t, err)
	t.Logf("enforcedTokens=%s", string(b))

	// With our synthetic logprobs, enforced tokens should be present and parseable.
	require.NotEmpty(t, enforcedTokens.Tokens)
}
