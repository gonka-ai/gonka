package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// pageRecordingStore records every GetDiffs window and fails one wider than
// a page.
type pageRecordingStore struct {
	storage.Storage
	mu    sync.Mutex
	calls [][2]uint64
}

func (s *pageRecordingStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	s.mu.Lock()
	s.calls = append(s.calls, [2]uint64{from, to})
	s.mu.Unlock()
	if to < from || to-from >= storage.DiffPageMaxNonces {
		return nil, fmt.Errorf("GetDiffs span %d..%d", from, to)
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

func (s *pageRecordingStore) ranges() [][2]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]uint64(nil), s.calls...)
}

type recoverFixture struct {
	store  *pageRecordingStore
	config types.SessionConfig
	group  []types.SlotAssignment
	user   *signing.Secp256k1Signer
	seed   *state.StateMachine
}

func newRecoverFixture(t *testing.T) *recoverFixture {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	inner := testutil.MustMemoryStore(t, defaultEscrowID, user.Address(), config, group, 10000)
	seed, err := state.NewStateMachine(defaultEscrowID, config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(), inner)
	require.NoError(t, err)
	return &recoverFixture{
		store:  &pageRecordingStore{Storage: inner},
		config: config,
		group:  group,
		user:   user,
		seed:   seed,
	}
}

// append applies nonce on the seed machine and stores the signed diff.
func (f *recoverFixture) append(t *testing.T, nonce uint64) {
	t.Helper()
	var txs []*types.DevshardTx
	if nonce == 1 {
		txs = []*types.DevshardTx{testutil.StartTx(1)}
	}
	root, err := f.seed.ApplyLocal(nonce, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, f.user, defaultEscrowID, nonce, txs, root)
	require.NoError(t, f.store.Storage.AppendDiff(defaultEscrowID, types.DiffRecord{Diff: diff, StateHash: root}))
}

func (f *recoverFixture) fresh(t *testing.T) *state.StateMachine {
	t.Helper()
	sm, err := state.NewStateMachine(defaultEscrowID, f.config, f.group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store)
	require.NoError(t, err)
	return sm
}

func requireSameRoot(t *testing.T, want, got *state.StateMachine) {
	t.Helper()
	wantRoot, err := want.ComputeStateRoot()
	require.NoError(t, err)
	gotRoot, err := got.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, wantRoot, gotRoot)
}

func TestRecoverHostState_ReplaysOnePageAtATime(t *testing.T) {
	f := newRecoverFixture(t)
	latest := uint64(storage.DiffPageMaxNonces) + 6
	for n := uint64(1); n <= latest; n++ {
		f.append(t, n)
	}

	sm := f.fresh(t)
	require.NoError(t, recoverHostState(f.store, sm, defaultEscrowID))
	require.Equal(t, latest, sm.LatestNonce())
	requireSameRoot(t, f.seed, sm)

	pageEnd := uint64(storage.DiffPageMaxNonces)
	require.Equal(t, [][2]uint64{{1, pageEnd}, {pageEnd + 1, latest}}, f.store.ranges(),
		"a journal over one page is replayed one page per read")
}

func TestRecoverHostState_SnapshotReplaysOnlyTheTail(t *testing.T) {
	f := newRecoverFixture(t)
	snapNonce := uint64(storage.DiffPageMaxNonces) + 2
	latest := snapNonce + 4
	for n := uint64(1); n <= snapNonce; n++ {
		f.append(t, n)
	}
	blob, err := host.MarshalStateSnapshotWithCommitted(f.seed.ExportState(), nil, nil, f.seed.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, f.store.Storage.SaveSnapshot(defaultEscrowID, snapNonce, blob))
	for n := snapNonce + 1; n <= latest; n++ {
		f.append(t, n)
	}

	sm := f.fresh(t)
	require.NoError(t, recoverHostState(f.store, sm, defaultEscrowID))
	require.Equal(t, latest, sm.LatestNonce())
	requireSameRoot(t, f.seed, sm)

	pageEnd := uint64(storage.DiffPageMaxNonces)
	require.Equal(t, [][2]uint64{
		{1, pageEnd}, {pageEnd + 1, snapNonce}, // height-sync fold of the restored snapshot
		{snapNonce + 1, latest}, // the tail after the snapshot is one page
	}, f.store.ranges())
}

func TestRecoverHostState_HoleStopsReplay(t *testing.T) {
	f := newRecoverFixture(t)
	for n := uint64(1); n <= 5; n++ {
		f.append(t, n)
	}
	require.NoError(t, f.store.Storage.AppendDiff(defaultEscrowID, types.DiffRecord{
		Diff: testutil.SignDiff(t, f.user, defaultEscrowID, 7, nil),
	}))

	sm := f.fresh(t)
	err := recoverHostState(f.store, sm, defaultEscrowID)
	var gap *storage.DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(6), gap.Expected)
	require.Equal(t, uint64(7), gap.Next)
	require.Equal(t, uint64(5), sm.LatestNonce(), "the contiguous prefix is applied, then replay stops at the hole")
}
