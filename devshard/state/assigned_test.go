package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestAssignedCountsStartsAcrossServiceNoncesAndTrialApply(t *testing.T) {
	for _, version := range []string{"v6", "v7"} {
		t.Run(version, func(t *testing.T) {
			hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
			user := testutil.MustGenerateKey(t)
			group := testutil.MakeGroup(hosts)
			cfg := testutil.DefaultConfig(3)
			newSM := func() *StateMachine {
				sm, err := NewStateMachine("escrow-1", cfg, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
					testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 10000), WithStateRootAndProtocolVersion(version))
				require.NoError(t, err)
				return sm
			}
			sm, replay := newSM(), newSM()
			for nonce := uint64(1); nonce <= 10; nonce++ {
				var txs []*types.DevshardTx
				if nonce == 1 || nonce == 4 {
					txs = []*types.DevshardTx{txStart(&types.MsgStartInference{InferenceId: nonce, Model: "llama", PromptHash: []byte("prompt"), InputLength: 100, MaxTokens: testutil.TestMaxTokens})}
				}
				before := sm.SnapshotState()
				preview, err := sm.PreviewLocalBestEffort(nonce, txs)
				require.NoError(t, err)
				require.Equal(t, before, sm.SnapshotState(), "trial apply must roll back the counter")
				require.True(t, sm.CommitValidated(preview))
				require.False(t, sm.CommitValidated(preview), "duplicate commit cannot count twice")
				diff := testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, txs, preview.Root)
				_, err = replay.ApplyDiff(diff)
				require.NoError(t, err)
				require.Equal(t, sm.SnapshotState(), replay.SnapshotState())
			}
			st := sm.SnapshotState()
			_, err := sm.ApplyLocal(11, []*types.DevshardTx{
				txStart(&types.MsgStartInference{InferenceId: 11, Model: "llama", PromptHash: []byte("prompt"), InputLength: 100, MaxTokens: testutil.TestMaxTokens}),
				txFinish(&types.MsgFinishInference{InferenceId: 999}),
			})
			require.Error(t, err)
			require.Equal(t, st, sm.SnapshotState(), "a rejected batch rolls back the accepted start")
			want := uint64(0)
			if version == "v7" {
				want = 2
			}
			require.Equal(t, want, st.HostStats[1].Assigned, "both real starts route to slot 1")
			require.Zero(t, st.HostStats[0].Assigned)
			require.Zero(t, st.HostStats[2].Assigned)
			roundTrip := types.EscrowStateFromProto(types.EscrowStateToProto(&st))
			require.Equal(t, st.HostStats, roundTrip.HostStats)
			restored := newSM()
			floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
			require.NoError(t, err)
			require.NoError(t, restored.RestoreStateWithFloor(roundTrip, floor))
			require.Equal(t, st.HostStats, restored.SnapshotState().HostStats)
		})
	}
}

func TestAssignedSnapshotRejectsMissingV7Count(t *testing.T) {
	st := &types.EscrowState{StateRootAndProtocolVersion: "v7", LatestNonce: 7,
		Group: []types.SlotAssignment{{SlotID: 0}}, HostStats: map[uint32]*types.HostStats{0: {}},
		Inferences: map[uint64]*types.InferenceRecord{1: {ExecutorSlot: 0}}}
	require.ErrorContains(t, validateAssignedSnapshot(st), "assigned")
	st.Inferences = nil
	st.SealedAcc = []byte{1}
	require.ErrorContains(t, validateAssignedSnapshot(st), "missing assigned")
	st.HostStats[0].Assigned = 1
	require.NoError(t, validateAssignedSnapshot(st))
}

func TestAssignedSurvivesSealingAndSettlementRestore(t *testing.T) {
	f := newJournalFixture(t)
	newSM := func(t *testing.T) *StateMachine {
		cfg := types.SessionConfig{TokenPrice: 1, VoteThreshold: 1, InferenceSealGraceSeconds: 3600,
			InferenceSealGraceNonces: 6, AutoSealEveryNNonces: 1}
		group := testutil.MakeGroup(f.hosts)
		sm, err := NewStateMachine(journalEscrowID, cfg, group, 1_000_000, f.user.Address(),
			signing.NewSecp256k1Verifier(),
			testutil.MustMemoryStore(t, journalEscrowID, f.user.Address(), cfg, group, 1_000_000),
			WithStateRootAndProtocolVersion("v7"))
		require.NoError(t, err)
		return sm
	}
	sm := runJournalPaths(t, newSM, f.signDiff, f.script(t), f.hosts, journalEscrowID)
	st := sm.SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)
	require.Empty(t, st.Inferences, "all records have been sealed")
	var assigned uint64
	for _, hs := range st.HostStats {
		assigned += hs.Assigned
	}
	require.Equal(t, uint64(6), assigned)
	roundTrip := types.EscrowStateFromProto(types.EscrowStateToProto(&st))
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	restored := newSM(t)
	require.NoError(t, restored.RestoreStateWithFloor(roundTrip, floor))
	got, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	want, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, st.HostStats, restored.SnapshotState().HostStats)
}
