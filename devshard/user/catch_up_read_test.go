package user

import (
	"context"
	"sync"
	"sync/atomic"
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

type countingDiffReadStore struct {
	*failDiffReadStore
	reads atomic.Int32
}

func (s *countingDiffReadStore) DiffSizes(escrowID string, from, to uint64, limit int) ([]storage.DiffSize, error) {
	s.reads.Add(1)
	return s.failDiffReadStore.DiffSizes(escrowID, from, to, limit)
}

func TestSession_CatchUpForSendReadsTheStoreOutsideTheSessionLock(t *testing.T) {
	store := &blockingDiffStore{Storage: storage.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	session := setupStoredSession(t, store)
	const n = 30
	composeEmptyDiffs(t, session, n)
	session.mu.Lock()
	for i := range session.group {
		session.hostSyncNonce[i] = n
	}
	session.dropDiffPrefixLocked()
	session.hostSyncNonce[0] = n - 20
	session.mu.Unlock()
	store.armed = true

	type result struct {
		diffs []types.Diff
		err   error
	}
	done := make(chan result, 1)
	go func() {
		diffs, err := session.catchUpForSend(context.Background(), 0, n)
		done <- result{diffs, err}
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
		t.Fatal("the inline catch-up store read held Session.mu")
	}
	close(store.release)
	res := <-done
	require.NoError(t, res.err)
	requireContiguous(t, res.diffs, n-19, n)
}

func TestSession_PrepareDoesNotReadTheStoreForARewoundHost(t *testing.T) {
	store := &countingDiffReadStore{failDiffReadStore: &failDiffReadStore{Storage: storage.NewMemory()}}
	session := setupStoredSession(t, store)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	for range 12 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	require.Greater(t, session.firstRetainedNonceLocked(), uint64(1))
	nextHost := int((session.nonce + 1) % uint64(len(session.group)))
	session.mu.Unlock()
	require.True(t, session.RewindHostCatchUp(nextHost, "test rewind"))

	store.fail.Store(true)
	store.reads.Store(0)
	prepared, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		return params, false, ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, nextHost, prepared.HostIdx())
	require.True(t, prepared.NeedsCatchUp())
	require.Empty(t, prepared.catchUp)
	require.Zero(t, store.reads.Load(), "prepare must not read the store under Session.mu")
	rec, ok := session.sm.GetInference(prepared.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)

	_, err = session.SendOnly(ctx, prepared, nil, nil)
	require.ErrorIs(t, err, errDiffRead)
	require.ErrorIs(t, err, ErrLocalCatchUpRead)

	store.fail.Store(false)
	resp, err := session.SendOnly(ctx, prepared, nil, nil)
	require.NoError(t, err)
	require.NoError(t, session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, prepared.Nonce(), session.hostSyncNonce[nextHost])
}

func TestSession_DeferredCatchUpCarriesTheTargetAfterTheCursorPassedIt(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	for range 12 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	nextHost := int((session.nonce + 1) % uint64(len(session.group)))
	session.mu.Unlock()
	require.True(t, session.RewindHostCatchUp(nextHost, "test rewind"))

	prepared, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		return params, false, ctx, nil
	})
	require.NoError(t, err)
	require.True(t, prepared.NeedsCatchUp())

	for range len(session.group) {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	require.Greater(t, session.hostSyncNonce[nextHost], prepared.Nonce())
	session.mu.Unlock()

	diffs, err := session.catchUpForSend(ctx, nextHost, prepared.Nonce())
	require.NoError(t, err)
	require.NotEmpty(t, diffs)
	require.Equal(t, prepared.Nonce(), diffs[0].Nonce)

	resp, err := session.SendOnly(ctx, prepared, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Receipt, "the host signs a receipt only when the request carries the target diff")
	require.NoError(t, session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
}
