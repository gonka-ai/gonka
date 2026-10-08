package state

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const journalEscrowID = "escrow-journal"

type journalFixture struct {
	hosts []*signing.Secp256k1Signer
	user  *signing.Secp256k1Signer
}

func newJournalFixture(t *testing.T) journalFixture {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 5)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	return journalFixture{hosts: hosts, user: testutil.MustGenerateKey(t)}
}

// machine matches TestCommittedEntriesHashMatchesMarshalPath: auto-seal on
// every nonce, a 6-nonce gate, and an hour of clock grace.
func (f journalFixture) machine(t *testing.T) *StateMachine {
	t.Helper()
	group := testutil.MakeGroup(f.hosts)
	config := types.SessionConfig{
		TokenPrice:                1,
		VoteThreshold:             1,
		InferenceSealGraceSeconds: 3600,
		InferenceSealGraceNonces:  6,
		AutoSealEveryNNonces:      1,
	}
	sm, err := NewStateMachine(journalEscrowID, config, group, 1_000_000, f.user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, journalEscrowID, f.user.Address(), config, group, 1_000_000))
	require.NoError(t, err)
	return sm
}

// script is the scripted session from TestCommittedEntriesHashMatchesMarshalPath:
// every transaction type, auto-seal of terminal records, finalize, and the
// settlement drain. Entry i is the diff at nonce i+1.
func (f journalFixture) script(t *testing.T) [][]*types.DevshardTx {
	t.Helper()
	h, e := f.hosts, journalEscrowID
	steps := [][]*types.DevshardTx{
		{startTx(1)},
		{confirmTx(t, h, e, 1)},
		{finishTx(t, h, e, 1)},
		{startTx(4)},
		{confirmTx(t, h, e, 4)},
		{finishTx(t, h, e, 4)},
		{validationTx(t, h, e, 4, 0, true), validationTx(t, h, e, 4, 0, true)},
		{startTx(8)},
		{confirmTx(t, h, e, 8)},
		{finishTx(t, h, e, 8)},
		{validationTx(t, h, e, 8, 4, false)},
		{voteTx(t, h, e, 8, 0, true)},
		{voteTx(t, h, e, 8, 1, true)},
		{startTx(14)},
		{confirmTx(t, h, e, 14)},
		{finishTx(t, h, e, 14)},
		{validationTx(t, h, e, 14, 0, false)},
		{voteTx(t, h, e, 14, 1, false)},
		{startTx(19)},
		{confirmTx(t, h, e, 19)},
		{timeoutTx(t, h, e, 19, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)},
		{startTx(22)},
		{confirmTx(t, h, e, 22)},
		{finishTx(t, h, e, 22)},
		{}, {}, {}, {},
		{txFinalize()},
		{}, {}, {}, {}, {},
	}
	return steps
}

func (f journalFixture) signDiff(t *testing.T, nonce uint64, txs []*types.DevshardTx, root []byte) types.Diff {
	t.Helper()
	return testutil.SignDiffWithRoot(t, f.user, journalEscrowID, nonce, txs, root)
}

// machineView is everything an apply can write, deep-copied.
type machineView struct {
	state     types.EscrowState
	committed map[uint64][]byte
	sealed    map[uint64]uint64
	xor       [32]byte
	root      []byte
}

func viewOf(t *testing.T, sm *StateMachine) machineView {
	t.Helper()
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	sm.mu.RLock()
	xor := sm.liveEntryXOR
	sm.mu.RUnlock()
	return machineView{
		state:     sm.SnapshotState(),
		committed: sm.ExportCommittedEntries(),
		sealed:    sm.ExportSealedNonces(),
		xor:       xor,
		root:      root,
	}
}

func TestJournalPathsMatchDirectApply(t *testing.T) {
	f := newJournalFixture(t)
	direct := runJournalPaths(t, f.machine, f.signDiff, f.script(t))
	require.Equal(t, types.PhaseSettlement, direct.Phase())
	require.Empty(t, direct.ExportCommittedEntries())
}

// runJournalPaths applies steps (entry i at nonce i+1) through ApplyLocal,
// PreviewLocalBestEffort + CommitValidated, and ValidateDiff + CommitValidated
// on three machines. After every diff the machines must be identical, and a
// trial apply must leave the live machine unchanged until it is committed.
func runJournalPaths(
	t *testing.T,
	newSM func(*testing.T) *StateMachine,
	sign func(*testing.T, uint64, []*types.DevshardTx, []byte) types.Diff,
	steps [][]*types.DevshardTx,
) *StateMachine {
	t.Helper()
	direct, preview, host := newSM(t), newSM(t), newSM(t)
	for i, txs := range steps {
		nonce := uint64(i + 1)
		root, err := direct.ApplyLocal(nonce, txs)
		require.NoError(t, err, "nonce %d", nonce)
		want := viewOf(t, direct)
		require.Equal(t, root, want.root)

		before := viewOf(t, preview)
		vd, err := preview.PreviewLocalBestEffort(nonce, txs)
		require.NoError(t, err, "nonce %d", nonce)
		require.Equal(t, root, vd.Root, "nonce %d", nonce)
		require.Len(t, vd.Applied, len(txs), "nonce %d", nonce)
		require.Equal(t, before, viewOf(t, preview), "preview changed live state at nonce %d", nonce)
		require.True(t, preview.CommitValidated(vd))
		require.Equal(t, want, viewOf(t, preview), "preview-commit diverged at nonce %d", nonce)

		before = viewOf(t, host)
		vd, err = host.ValidateDiff(sign(t, nonce, txs, root))
		require.NoError(t, err, "nonce %d", nonce)
		require.Equal(t, before, viewOf(t, host), "validate changed live state at nonce %d", nonce)
		require.True(t, host.CommitValidated(vd))
		require.Equal(t, want, viewOf(t, host), "validate-commit diverged at nonce %d", nonce)
	}
	return direct
}

// A later tx that fails must undo the record and host-stats writes of an
// earlier tx in the same diff.
func TestJournalUndoesEarlierTxInFailedDiff(t *testing.T) {
	f := newJournalFixture(t)
	sm := f.machine(t)
	steps := f.script(t)
	for i := 0; i < 2; i++ {
		_, err := sm.ApplyLocal(uint64(i+1), steps[i])
		require.NoError(t, err)
	}
	before := viewOf(t, sm)
	sm.mu.RLock()
	recPtr := sm.state.Inferences[1]
	statsPtr := sm.state.HostStats[1]
	sm.mu.RUnlock()

	missing := validationTx(t, f.hosts, journalEscrowID, 999, 0, true)
	_, err := sm.ApplyLocal(3, []*types.DevshardTx{finishTx(t, f.hosts, journalEscrowID, 1), missing})
	require.ErrorIs(t, err, types.ErrInferenceNotFound)

	require.Equal(t, before, viewOf(t, sm))
	require.Equal(t, types.StatusStarted, before.state.Inferences[1].Status)
	sm.mu.RLock()
	require.Same(t, recPtr, sm.state.Inferences[1])
	require.Same(t, statsPtr, sm.state.HostStats[1])
	require.Nil(t, sm.journal)
	sm.mu.RUnlock()
}

// Preview copies only the records it writes. Untouched records and committed
// blobs keep their identity, and a commit installs the written copies.
func TestJournalPreviewKeepsUntouchedObjects(t *testing.T) {
	f := newJournalFixture(t)
	sm := f.machine(t)
	steps := f.script(t)
	for i := 0; i < 5; i++ {
		_, err := sm.ApplyLocal(uint64(i+1), steps[i])
		require.NoError(t, err)
	}
	// Live: 1 Finished, 4 Started.
	type identity struct {
		rec  *types.InferenceRecord
		blob *byte
	}
	snapshotIdentity := func() map[uint64]identity {
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		out := make(map[uint64]identity, len(sm.state.Inferences))
		for id, rec := range sm.state.Inferences {
			out[id] = identity{rec: rec, blob: unsafe.SliceData(sm.committedEntries[id])}
		}
		return out
	}
	before := snapshotIdentity()
	beforeView := viewOf(t, sm)

	vd, err := sm.PreviewLocalBestEffort(6, steps[5])
	require.NoError(t, err)
	require.Equal(t, before, snapshotIdentity())
	require.Equal(t, beforeView, viewOf(t, sm))

	require.True(t, sm.CommitValidated(vd))
	after := snapshotIdentity()
	require.Equal(t, before[1], after[1], "id 1 is not in the diff")
	require.NotSame(t, before[4].rec, after[4].rec, "finish writes a copy of id 4")
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[4].Status)
	got, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, vd.Root, got)
}

// A commit for a nonce another writer already applied is refused and leaves
// that writer's records in place.
func TestJournalStaleCommitKeepsOtherWriter(t *testing.T) {
	f := newJournalFixture(t)
	sm := f.machine(t)
	steps := f.script(t)
	for i := 0; i < 2; i++ {
		_, err := sm.ApplyLocal(uint64(i+1), steps[i])
		require.NoError(t, err)
	}
	stale, err := sm.PreviewLocalBestEffort(3, steps[2])
	require.NoError(t, err)

	timeout := timeoutTx(t, f.hosts, journalEscrowID, 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{timeout})
	require.NoError(t, err)
	want := viewOf(t, sm)

	require.False(t, sm.CommitValidated(stale))
	require.Equal(t, want, viewOf(t, sm))
	require.Equal(t, types.StatusTimedOut, want.state.Inferences[1].Status)
}
