package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// obsCallStore counts the destructive obs rebuild entry point, and can hold it
// open so a test can observe recovery finishing without it.
type obsCallStore struct {
	storage.Storage
	mu      sync.Mutex
	clears  int
	entered chan struct{}
	release chan struct{}
}

func (s *obsCallStore) ClearValidationObs(escrowID string) error {
	s.mu.Lock()
	s.clears++
	s.mu.Unlock()
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	if s.release != nil {
		<-s.release
	}
	return s.Storage.ClearValidationObs(escrowID)
}

func (s *obsCallStore) clearCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clears
}

type sealedDeleteStore struct {
	storage.Storage
	mu      sync.Mutex
	deletes int
	entered chan struct{}
	release chan struct{}
}

func (s *sealedDeleteStore) DeleteSealedInferences(escrowID string) error {
	s.mu.Lock()
	s.deletes++
	entered := s.entered
	s.entered = nil
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if s.release != nil {
		<-s.release
	}
	return s.Storage.DeleteSealedInferences(escrowID)
}

func (s *sealedDeleteStore) deleteCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes
}

func populateFinishedAndSeal(t *testing.T, store storage.Storage) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine("1", config, group, 100000, user.Address(), verifier, store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)

	apply := func(nonce uint64, txs []*types.DevshardTx) {
		t.Helper()
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		diff := signDiffWithRoot(t, user, "1", nonce, txs, root)
		require.NoError(t, store.AppendDiff("1", types.DiffRecord{Diff: diff, StateHash: root}))
	}

	start := startTx(1)
	apply(1, []*types.DevshardTx{start})
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "1", 1, start.GetStartInference().GetPromptHash(),
		"llama", 100, testutil.TestMaxTokens, 1000, 2000)
	apply(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}})
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"), InputTokens: 10, OutputTokens: 20,
		ExecutorSlot: 1, EscrowId: "1",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	apply(3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}})
	require.NoError(t, sm.SealInference(1))
	return group, user, hosts[0]
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
	return waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
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

// saveMismatchedSnapshot writes a decodable snapshot whose state is only
// advanced to stateThrough while claiming to be at claimNonce, i.e. a blob that
// decodes cleanly but does not reproduce the journal's root at its own nonce.
func saveMismatchedSnapshot(t *testing.T, store storage.Storage, stateThrough, claimNonce uint64) {
	t.Helper()
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	sm, err := state.NewStateMachine("1", meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, signing.NewSecp256k1Verifier(), store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	records, err := store.GetDiffs("1", 1, stateThrough)
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	require.NoError(t, saveHostSnapshot(store, sm, "1", claimNonce))
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

	require.Equal(t, []diffRange{
		{1, 7},  // RestoreStateWithFloor rebuilds the height-sync floor from the journal
		{7, 7},  // root check against the journal at the snapshot nonce
		{8, 10}, // the tail is the only range applied: obs tops up from it too
	}, store.ranges())
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
	require.Equal(t, []diffRange{{1, 10}, {10, 10}}, store.ranges(),
		"a current snapshot rebuilds the height-sync floor from the journal, then reads one diff for the root check")
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
	escrowState, committed, sealed, _, err := host.UnmarshalStateSnapshotWithCommitted(data)
	require.NoError(t, err)
	require.Equal(t, uint64(10), escrowState.LatestNonce)
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

// A snapshot is not self-authenticating and the store can be shared between
// hosts, so a blob that does not reproduce the journal's root at its own nonce
// must be discarded rather than trusted for nonces 1..N.
func TestRecoverSessions_SnapshotRootMismatchReplaysFromOne(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveMismatchedSnapshot(t, inner, 5, 7)

	store := &recordingStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	require.Equal(t, []diffRange{{1, 5}, {7, 7}, {1, 10}}, store.ranges(),
		"a rejected snapshot must fall back to a full replay")

	got := recoveredHostState(t, mgr)
	want := fullReplayState(t, inner)
	require.Equal(t, want.LatestNonce, got.LatestNonce)
	require.Equal(t, want.Balance, got.Balance)
	require.Equal(t, want.Phase, got.Phase)
	require.Len(t, got.Inferences, len(want.Inferences))
}

// The rejected snapshot's partial state must not leak into the replay: a state
// machine that was already restored is thrown away, not replayed on top of.
func TestRecoverSessions_RejectedSnapshotDoesNotPoisonReplay(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 8)
	saveMismatchedSnapshot(t, inner, 3, 6)

	mgr := recoverTestManager(t, inner, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())

	got := recoveredHostState(t, mgr)
	want := fullReplayState(t, inner)
	require.Equal(t, want.LatestNonce, got.LatestNonce)
	require.Equal(t, want.Balance, got.Balance)
	require.Equal(t, want.SealedAcc, got.SealedAcc)
}

// Validation obs rows are written by the live apply path and are durable, so
// the history a snapshot covers is already recorded. Clearing and rebuilding it
// would delete good rows, re-read the whole journal, and pay a write
// transaction per historical seal to rewrite what was already there.
func TestRecoverSessions_SnapshotPathLeavesValidationObsAlone(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveSnapshotThrough(t, inner, 7)

	store := &obsCallStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	mgr.WaitRecoveryRepairs()

	require.Zero(t, store.clearCalls(), "a restored snapshot must not clear durable obs rows")
}

// Replaying the whole journal is the one case that must rebuild: ApplyLocal
// records no obs, and the clear-then-replay is what repairs batches the live
// path dropped under backpressure. The rebuild runs in the background, so it
// must not have touched obs by the time recovery returns.
func TestRecoverSessions_FullReplayRebuildsValidationObsInBackground(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)

	store := &obsCallStore{Storage: inner, release: make(chan struct{})}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	t.Cleanup(func() {
		select {
		case <-store.release:
		default:
			close(store.release)
		}
	})

	// The rebuild is pinned inside ClearValidationObs, so recovery can only
	// return if it no longer waits for it.
	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("recovery blocked on the background obs rebuild")
	}
	require.Equal(t, 1, mgr.loadedSessionCount(), "the session must be published before the rebuild finishes")

	close(store.release)
	mgr.WaitRecoveryRepairs()
	require.Equal(t, 1, store.clearCalls(), "the background rebuild must still run")
}

func TestRecoverSessions_SnapshotPathLeavesSealedInferenceRows(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveSnapshotThrough(t, inner, 7)

	planted := storage.InferenceRow{
		InferenceID: 99, SealedNonce: 7, ObsPresent: true,
		SealedModel: "llama", SealedInputTokens: 10, SealedOutputTokens: 20,
	}
	require.NoError(t, inner.InsertSealedInference("1", planted))

	store := &sealedDeleteStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	mgr.WaitRecoveryRepairs()

	require.Zero(t, store.deleteCalls(), "a restored snapshot must not wipe sealed-inference rows")
	after, ok, err := inner.GetSealedInference("1", 99)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, planted, after)
}

func TestRecoverSessions_FullReplayRebuildsSealedIndexInBackground(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateFinishedAndSeal(t, inner)

	store := &sealedDeleteStore{Storage: inner, release: make(chan struct{})}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	t.Cleanup(func() {
		select {
		case <-store.release:
		default:
			close(store.release)
		}
	})

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("recovery blocked on the background sealed-index rebuild")
	}
	require.Equal(t, 1, mgr.loadedSessionCount(), "the session must be published before the rebuild finishes")

	close(store.release)
	mgr.WaitRecoveryRepairs()
	require.Equal(t, 1, store.deleteCalls(), "the background rebuild must still run")

	row, ok, err := inner.GetSealedInference("1", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.ObsPresent, "full-replay from-diffs must restore rich rows")
	require.Equal(t, "llama", row.SealedModel)
}

func TestStartRecovery_CompleteAfterSealedIndexRepair(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateFinishedAndSeal(t, inner)

	entered := make(chan struct{})
	store := &sealedDeleteStore{Storage: inner, entered: entered, release: make(chan struct{})}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	t.Cleanup(func() {
		select {
		case <-store.release:
		default:
			close(store.release)
		}
	})

	wait := mgr.StartRecovery(context.Background())
	wait()
	require.Equal(t, 1, mgr.loadedSessionCount(), "the session is published before the rebuild finishes")
	progress := mgr.RecoveryProgressSnapshot()
	require.False(t, progress.Complete, "recovery_complete waits for the sealed-index rebuild, not just the backlog")
	require.Equal(t, int64(1), progress.Total)
	require.Equal(t, int64(1), progress.Recovered)
	require.Positive(t, progress.Pending)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sealed-index rebuild never started")
	}
	close(store.release)
	mgr.WaitRecoveryRepairs()
	progress = mgr.RecoveryProgressSnapshot()
	require.True(t, progress.Complete)
	require.Zero(t, progress.Pending)
}

func TestRecoverSessions_RejectedSnapshotRebuildsSealedIndex(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 10)
	saveMismatchedSnapshot(t, inner, 5, 7)

	store := &sealedDeleteStore{Storage: inner}
	mgr := recoverTestManager(t, store, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	mgr.WaitRecoveryRepairs()
	require.Equal(t, 1, store.deleteCalls(), "a rejected snapshot must take the full-replay sealed rebuild")
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
	require.Len(t, got.Inferences, len(want.Inferences))
	for id, rec := range want.Inferences {
		gotRec, ok := got.Inferences[id]
		require.True(t, ok, "inference %d missing after snapshot+tail recovery", id)
		require.Equal(t, rec.Status, gotRec.Status)
		require.Equal(t, rec.MaxTokens, gotRec.MaxTokens)
		require.Equal(t, rec.ReservedCost, gotRec.ReservedCost)
	}
}
