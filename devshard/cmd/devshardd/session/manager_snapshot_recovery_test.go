package session

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type diffRange struct {
	from, to uint64
}

type recordingStore struct {
	storage.Storage
	mu    sync.Mutex
	calls []diffRange
}

func (s *recordingStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	s.mu.Lock()
	s.calls = append(s.calls, diffRange{fromNonce, toNonce})
	s.mu.Unlock()
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

func (s *recordingStore) ranges() []diffRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]diffRange, len(s.calls))
	copy(out, s.calls)
	return out
}

type loadSnapshotErrStore struct {
	storage.Storage
	err error
}

func (s *loadSnapshotErrStore) LoadSnapshot(string) (uint64, []byte, error) {
	return 0, nil, s.err
}

type snapshotBlobStore struct {
	storage.Storage
	nonce uint64
	data  []byte
}

func (s *snapshotBlobStore) LoadSnapshot(string) (uint64, []byte, error) {
	return s.nonce, s.data, nil
}

func recoverTestManager(t *testing.T, store storage.Storage, hostSigner *signing.Secp256k1Signer, user *signing.Secp256k1Signer, group []types.SlotAssignment) *HostManager {
	t.Helper()
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	return NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
}

func saveSnapshotThrough(t *testing.T, store storage.Storage, through uint64) {
	t.Helper()
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	sm, err := state.NewStateMachine("1", meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, signing.NewSecp256k1Verifier(), store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	records, err := store.GetDiffs("1", 1, through)
	require.NoError(t, err)
	require.Len(t, records, int(through))
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	require.NoError(t, saveHostSnapshot(store, sm, "1", through))
}

func recoveredHostState(t *testing.T, mgr *HostManager) types.EscrowState {
	t.Helper()
	mgr.sessionsMutex.RLock()
	defer mgr.sessionsMutex.RUnlock()
	srv, ok := mgr.sessions["1"]
	require.True(t, ok, "session must exist after recovery")
	require.NotNil(t, srv.Host())
	return srv.Host().SnapshotState()
}

func fullReplayState(t *testing.T, store storage.Storage) types.EscrowState {
	t.Helper()
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	sm, err := state.NewStateMachine("1", meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, signing.NewSecp256k1Verifier(), store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	if meta.LatestNonce == 0 {
		return sm.SnapshotState()
	}
	records, err := store.GetDiffs("1", 1, meta.LatestNonce)
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	return sm.SnapshotState()
}

func TestRecoverSessions_RestoresSnapshotAndReplaysOnlyTail(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveSnapshotThrough(t, inner, 7)

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	want := fullReplayState(t, inner)
	require.Equal(t, want.LatestNonce, got.LatestNonce)
	require.Equal(t, want.Balance, got.Balance)
	require.Equal(t, want.Phase, got.Phase)

	calls := store.ranges()
	require.NotEmpty(t, calls)
	require.Equal(t, diffRange{8, 10}, calls[0], "apply path must fetch only post-snapshot diffs")
	require.Equal(t, diffRange{1, 10}, calls[1], "obs rebuild still reads the journal")
}

func TestRecoverSessions_SnapshotCurrentSkipsDiffApply(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveSnapshotThrough(t, inner, 10)

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	require.Equal(t, uint64(10), got.LatestNonce)
	require.Equal(t, []diffRange{{1, 10}}, store.ranges(), "current snapshot applies nothing; obs still rebuilds from the journal")
}

func TestRecoverSessions_NoSnapshotReplaysFromOne(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	require.Equal(t, uint64(10), got.LatestNonce)
	require.Equal(t, []diffRange{{1, 10}}, store.ranges())
}

func TestRecoverSessions_SavesSnapshotAfterFullReplay(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 10)
	_, _, err := store.LoadSnapshot("1")
	require.ErrorIs(t, err, storage.ErrSnapshotNotFound)

	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	nonce, data, err := store.LoadSnapshot("1")
	require.NoError(t, err)
	require.Equal(t, uint64(10), nonce)
	state, committed, sealed, err := host.UnmarshalStateSnapshotWithCommitted(data)
	require.NoError(t, err)
	require.Equal(t, uint64(10), state.LatestNonce)
	require.NotNil(t, committed)
	_ = sealed
}

func TestRecoverSessions_CorruptSnapshotReplaysFromOne(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 6)
	require.NoError(t, inner.SaveSnapshot("1", 4, []byte("not-a-protobuf-snapshot")))

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	require.Equal(t, uint64(6), got.LatestNonce)
	require.Equal(t, []diffRange{{1, 6}}, store.ranges(), "undecodable snapshot must fall back to a full replay")
}

func TestRecoverSessions_SnapshotAheadOfLatestIgnored(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 5)
	saveSnapshotThrough(t, inner, 5)
	require.NoError(t, inner.SaveSnapshot("1", 99, []byte("future-nonce-blob")))

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	require.Equal(t, uint64(5), got.LatestNonce)
	require.Equal(t, []diffRange{{1, 5}}, store.ranges(), "snapshot nonce past latest_nonce must be ignored")
}

func TestRecoverSessions_LoadSnapshotErrorReplaysFromOne(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 4)
	store := &loadSnapshotErrStore{Storage: inner, err: errors.New("sqlite busy")}

	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	require.Equal(t, uint64(4), recoveredHostState(t, mgr).LatestNonce)
}

func TestRecoverSessions_EmptySnapshotBlobReplaysFromOne(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 3)
	store := &recordingStore{Storage: &snapshotBlobStore{Storage: inner, nonce: 2, data: nil}}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	require.Equal(t, uint64(3), recoveredHostState(t, mgr).LatestNonce)
	require.Equal(t, []diffRange{{1, 3}}, store.ranges())
}

func TestRecoverSessions_SnapshotMatchesFullReplayWithLiveInferences(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 12)
	saveSnapshotThrough(t, inner, 5)

	mgr := recoverTestManager(t, inner, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	want := fullReplayState(t, inner)
	require.Equal(t, want.LatestNonce, got.LatestNonce)
	require.Equal(t, want.Balance, got.Balance)
	require.Equal(t, len(want.Inferences), len(got.Inferences))
	for id, rec := range want.Inferences {
		gotRec, ok := got.Inferences[id]
		require.True(t, ok, "inference %d missing after snapshot+tail recovery", id)
		require.Equal(t, rec.Status, gotRec.Status)
		require.Equal(t, rec.MaxTokens, gotRec.MaxTokens)
		require.Equal(t, rec.ReservedCost, gotRec.ReservedCost)
	}
}
