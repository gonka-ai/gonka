package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type countingGetDiffsStore struct {
	storage.Storage
	gets atomic.Int32
}

func (s *countingGetDiffsStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	s.gets.Add(1)
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

// pagingGetDiffsStore records every range and rejects a call wider than one page.
type pagingGetDiffsStore struct {
	storage.Storage
	mu    sync.Mutex
	calls [][2]uint64
	onGet func(from, to uint64)
}

func (s *pagingGetDiffsStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	s.mu.Lock()
	s.calls = append(s.calls, [2]uint64{from, to})
	hook := s.onGet
	s.mu.Unlock()
	if to < from || to-from >= storage.DiffPageMaxNonces {
		return nil, fmt.Errorf("GetDiffs span %d..%d", from, to)
	}
	if hook != nil {
		hook(from, to)
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

func (s *pagingGetDiffsStore) ranges() [][2]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]uint64, len(s.calls))
	copy(out, s.calls)
	return out
}

// seedEscrowDiffs writes signed diffs 1..through into store using a throwaway SM.
// Returns the signed Diff values for later catch-up requests.
func seedEscrowDiffs(t *testing.T, store storage.Storage, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, through uint64) []types.Diff {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)

	out := make([]types.Diff, 0, through)
	for n := uint64(1); n <= through; n++ {
		var txs []*types.DevshardTx
		if n == 1 {
			txs = []*types.DevshardTx{testutil.StartTx(1)}
		}
		root, err := sm.ApplyLocal(n, txs)
		require.NoError(t, err)
		diff := testutil.SignDiffWithRoot(t, user, "escrow-1", n, txs, root)
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
			Diff: diff, StateHash: root,
		}))
		out = append(out, diff)
	}
	return out
}

// newHostAtNonce builds a Host whose in-memory SM is at memNonce while store
// already holds durable diffs through durableThrough (must be >= memNonce).
func newHostAtNonce(
	t *testing.T,
	store storage.Storage,
	user *signing.Secp256k1Signer,
	hosts []*signing.Secp256k1Signer,
	memNonce, durableThrough uint64,
) *Host {
	t.Helper()
	require.LessOrEqual(t, memNonce, durableThrough)

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))
	allDiffs := seedEscrowDiffs(t, store, user, hosts, durableThrough)

	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	for _, d := range allDiffs {
		if d.Nonce > memNonce {
			break
		}
		_, err := sm.ApplyDiff(d)
		require.NoError(t, err)
	}
	require.Equal(t, memNonce, sm.LatestNonce())

	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)
	return h
}

func TestHost_CatchUpFromStore_FastForwardsMemory(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	store := storage.NewMemory()

	const memNonce, durableN uint64 = 2, 5
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)
	require.Equal(t, memNonce, h.LatestNonce())

	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce())

	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce(), "already-caught-up host must be a no-op")
}

func TestHost_ReconcileFastForwardOnGap(t *testing.T) {
	// Standby RAM at K=2, durable PG at N=5, catch-up starts at M+1=4 (K<M<N).
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &countingGetDiffsStore{Storage: inner}

	const memNonce, durableN uint64 = 2, 5
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)

	// Re-read durable diffs for the catch-up request (nonces 4 and 5).
	recs, err := inner.GetDiffs("escrow-1", 4, 5)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	store.gets.Store(0) // reset after setup reads

	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{recs[0].Diff, recs[1].Diff},
	})
	require.NoError(t, err)
	require.Equal(t, durableN, resp.Nonce)
	require.Equal(t, durableN, h.LatestNonce())
	require.GreaterOrEqual(t, store.gets.Load(), int32(1), "gap must trigger GetDiffs")

	// Durable row count unchanged (fast-forward must not re-insert).
	all, err := inner.GetDiffs("escrow-1", 1, durableN)
	require.NoError(t, err)
	require.Len(t, all, int(durableN))
}

func TestHost_ReconcileHappyPath_NoGetDiffs(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &countingGetDiffsStore{Storage: inner}

	h := newHostAtNonce(t, store, user, hosts, 2, 2)
	store.gets.Store(0)

	// Contiguous next nonce — no gap, no store read.
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, inner)
	require.NoError(t, err)
	for _, rec := range mustGetDiffs(t, inner, 1, 2) {
		_, err := sm.ApplyDiff(rec.Diff)
		require.NoError(t, err)
	}
	root, err := sm.ApplyLocal(3, nil)
	require.NoError(t, err)
	diff3 := testutil.SignDiffWithRoot(t, user, "escrow-1", 3, nil, root)

	store.gets.Store(0)
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff3}})
	require.NoError(t, err)
	require.Equal(t, int32(0), store.gets.Load(), "happy path must not GetDiffs")
	require.Equal(t, uint64(3), h.LatestNonce())
}

func TestHost_CatchUpFromStore_PagesAMultiPageGap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &pagingGetDiffsStore{Storage: inner}

	const memNonce uint64 = 1
	durableN := uint64(storage.DiffPageMaxNonces) + 2
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)
	store.mu.Lock()
	store.calls = nil
	store.mu.Unlock()

	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce())

	pageEnd := memNonce + storage.DiffPageMaxNonces
	require.Equal(t, [][2]uint64{{memNonce + 1, pageEnd}, {pageEnd + 1, durableN}}, store.ranges(),
		"one read per page, and no nonce is read twice")
}

func TestHost_ReconcileFastForward_PagesAMultiPageGap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &pagingGetDiffsStore{Storage: inner}

	const memNonce uint64 = 1
	durableN := uint64(storage.DiffPageMaxNonces) + 3
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)
	tip := mustGetDiffs(t, inner, durableN, durableN)
	require.Len(t, tip, 1)
	store.mu.Lock()
	store.calls = nil
	store.mu.Unlock()

	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{tip[0].Diff}})
	require.NoError(t, err)
	require.Equal(t, durableN, resp.Nonce)
	require.Equal(t, durableN, h.LatestNonce())

	pageEnd := memNonce + storage.DiffPageMaxNonces
	require.Equal(t, [][2]uint64{{memNonce + 1, pageEnd}, {pageEnd + 1, durableN - 1}}, store.ranges(),
		"the gap is two page reads; the incoming nonce is applied without another journal read")
}

func TestHost_CatchUpFromStore_ConcurrentApplyIsNotRepeated(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &pagingGetDiffsStore{Storage: inner}

	const memNonce, durableN uint64 = 1, 6
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)

	var once sync.Once
	store.onGet = func(from, to uint64) {
		once.Do(func() {
			rec := mustGetDiffs(t, inner, from, from)
			require.Len(t, rec, 1)
			h.mu.Lock()
			defer h.mu.Unlock()
			require.Equal(t, memNonce, h.sm.LatestNonce())
			require.NoError(t, h.applyDurableRecordLocked(rec[0]))
			require.Equal(t, memNonce+1, h.sm.LatestNonce())
		})
	}

	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce())
}

func TestHost_CatchUpFromStore_ConcurrentCloseReturnsNil(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &pagingGetDiffsStore{Storage: inner}

	const memNonce, durableN uint64 = 1, 5
	h := newHostAtNonce(t, store, user, hosts, memNonce, durableN)
	rest := mustGetDiffs(t, inner, memNonce+1, durableN)

	var once sync.Once
	store.onGet = func(uint64, uint64) {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, rec := range rest {
				require.NoError(t, h.applyDurableRecordLocked(rec))
			}
			require.Equal(t, durableN, h.sm.LatestNonce())
		})
	}

	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce())
	require.NoError(t, h.CatchUpFromStore(context.Background()))
	require.Equal(t, durableN, h.LatestNonce())
}

func TestHost_ReconcileGapIncomplete_StopsAtHole(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	store := storage.NewMemory()

	// Durable only through 2; memory at 1; request jumps to 5. Nonce 2 is a
	// contiguous page and is applied. Nonce 3 is a hole, so the fast-forward
	// stops there instead of jumping to 5.
	h := newHostAtNonce(t, store, user, hosts, 1, 2)

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	for _, rec := range mustGetDiffs(t, store, 1, 2) {
		_, err := sm.ApplyDiff(rec.Diff)
		require.NoError(t, err)
	}
	var root []byte
	for n := uint64(3); n <= 5; n++ {
		root, err = sm.ApplyLocal(n, nil)
		require.NoError(t, err)
	}
	diff5 := testutil.SignDiffWithRoot(t, user, "escrow-1", 5, nil, root)

	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff5}})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReconcileGap), "got %v", err)
	require.Equal(t, uint64(2), h.LatestNonce(), "the contiguous prefix is applied; nonces after the hole are not")
}

func TestHost_ReconcileGapAtStart_DoesNotApply(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	store := storage.NewMemory()

	h := newHostAtNonce(t, store, user, hosts, 2, 2)

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	for _, rec := range mustGetDiffs(t, store, 1, 2) {
		_, err := sm.ApplyDiff(rec.Diff)
		require.NoError(t, err)
	}
	var root []byte
	for n := uint64(3); n <= 5; n++ {
		root, err = sm.ApplyLocal(n, nil)
		require.NoError(t, err)
	}
	diff5 := testutil.SignDiffWithRoot(t, user, "escrow-1", 5, nil, root)

	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff5}})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReconcileGap), "got %v", err)
	require.Equal(t, uint64(2), h.LatestNonce())
}

func mustGetDiffs(t *testing.T, store storage.Storage, from, to uint64) []types.DiffRecord {
	t.Helper()
	recs, err := store.GetDiffs("escrow-1", from, to)
	require.NoError(t, err)
	return recs
}

func TestDurableRangeComplete(t *testing.T) {
	recs := []types.DiffRecord{
		{Diff: types.Diff{Nonce: 3}},
		{Diff: types.Diff{Nonce: 4}},
		{Diff: types.Diff{Nonce: 5}},
	}
	require.True(t, durableRangeComplete(recs, 3, 5))
	require.False(t, durableRangeComplete(recs[:2], 3, 5))
	require.False(t, durableRangeComplete(recs, 2, 5))
	require.True(t, durableRangeComplete(nil, 5, 4))
}
