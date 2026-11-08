package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"
)

// blockingDiffStore holds GetDiffs until release is closed once armed.
type blockingDiffStore struct {
	storage.Storage
	armed   bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDiffStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	if s.armed {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

func TestSession_CatchUpStoreReadDoesNotHoldTheSessionLock(t *testing.T) {
	store := &blockingDiffStore{Storage: storage.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	session := setupStoredSession(t, store)
	const n = maxRetainedDiffs + 40
	composeEmptyDiffs(t, session, n)
	store.armed = true

	done := make(chan error, 1)
	go func() { done <- session.sendCatchUpWith(context.Background(), 0, session.clients[0]) }()
	<-store.entered

	locked := make(chan struct{})
	go func() {
		session.mu.Lock()
		session.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(store.release)
		t.Fatal("a catch-up store read held Session.mu")
	}
	close(store.release)
	require.NoError(t, <-done)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(n), session.hostSyncNonce[0])
}

func TestSession_CatchUpForSendInlineStoreReadDoesNotHoldTheSessionLock(t *testing.T) {
	store := &blockingDiffStore{Storage: storage.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	session := setupStoredSession(t, store)
	const n = maxRetainedDiffs + 10
	composeEmptyDiffs(t, session, n)
	session.mu.Lock()
	for i := range session.group {
		session.hostSyncNonce[i] = n
	}
	session.dropDiffPrefixLocked()
	session.hostSyncNonce[0] = n - 20 // < catchUpChunkSize, below firstRetainedNonceLocked
	session.mu.Unlock()
	store.armed = true

	done := make(chan error, 1)
	go func() {
		_, err := session.catchUpForSend(context.Background(), 0, n)
		done <- err
	}()
	<-store.entered

	locked := make(chan struct{})
	go func() {
		session.mu.Lock()
		session.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(store.release)
		t.Fatal("catchUpForSend inline store read held Session.mu")
	}
	close(store.release)
	require.NoError(t, <-done)
}

func TestSession_FailedInlineCatchUpReadReturnsPreparedInferenceWithFarBehind(t *testing.T) {
	store := &failDiffReadStore{Storage: storage.NewMemory()}
	session := setupStoredSession(t, store)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Advance all three hosts past nonce 12 and let the session trim nonces 1..11
	// once every cursor has moved past them, then rewind the next target host to 0
	// (< catchUpChunkSize, pointing into history only the store holds).
	for range 12 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	tip := session.nonce
	require.Greater(t, session.firstRetainedNonceLocked(), uint64(1))
	nextHost := int((tip + 1) % uint64(len(session.group)))
	session.mu.Unlock()
	require.True(t, session.RewindHostCatchUp(nextHost, "test rewind"))

	// 1. Transient store failure during PrepareInferenceFn: returns PreparedInference
	// with farBehind=true instead of dropping the committed handle, and SendOnly
	// succeeds once the store recovers.
	store.fail.Store(true)
	prepared, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		return params, false, ctx, nil
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.True(t, prepared.farBehind)
	require.Empty(t, prepared.catchUp)

	store.fail.Store(false)
	resp, err := session.SendOnly(ctx, prepared, nil, nil)
	require.NoError(t, err)
	require.NoError(t, session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
	_, err = session.SendInference(ctx, params)
	require.NoError(t, err)
	rec, ok := session.sm.GetInference(prepared.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)

	// 2. Persistent store failure across PrepareInferenceFn and SendOnly (probe=true):
	// PrepareInferenceFn still returns PreparedInference so callers keep the nonce,
	// probe flag, and refusal payload, while SendOnly surfaces errDiffRead.
	session.mu.Lock()
	nextHost2 := int((session.nonce + 1) % uint64(len(session.group)))
	session.mu.Unlock()
	require.True(t, session.RewindHostCatchUp(nextHost2, "test rewind 2"))

	store.fail.Store(true)
	prepared2, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		return params, true, ctx, nil
	})
	require.NoError(t, err)
	require.NotNil(t, prepared2)
	require.True(t, prepared2.farBehind)
	require.True(t, prepared2.IsProbe())
	require.NotNil(t, prepared2.Payload())

	_, err = session.SendOnly(ctx, prepared2, nil, nil)
	require.ErrorIs(t, err, errDiffRead)
}
