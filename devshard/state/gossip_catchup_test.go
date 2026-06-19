package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

// applyDiffPersist applies a user-signed diff, persists the record (txs +
// state hash + warm-key delta) the same way gossip recovery / RecoverSession
// replays from storage — see §1.5 in devshard/docs/inferences-pruning.md.
func applyDiffPersist(
	t *testing.T,
	sm *StateMachine,
	store *storage.Memory,
	user *signing.Secp256k1Signer,
	escrowID string,
	txs []*types.DevshardTx,
) {
	t.Helper()
	warmBefore := sm.WarmKeys()
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, escrowID, nonce, txs)
	root, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	delta := types.ComputeWarmKeyDelta(warmBefore, sm.WarmKeys())
	require.NoError(t, store.AppendDiff(escrowID, types.DiffRecord{
		Diff:         diff,
		StateHash:    root,
		WarmKeyDelta: delta,
	}))
}

func copyDiffJournal(t *testing.T, src, dst *storage.Memory, escrowID string, from, to uint64) {
	t.Helper()
	recs, err := src.GetDiffs(escrowID, from, to)
	require.NoError(t, err)
	for _, rec := range recs {
		require.NoError(t, dst.AppendDiff(escrowID, types.DiffRecord{
			Diff:         rec.Diff,
			StateHash:    append([]byte(nil), rec.StateHash...),
			WarmKeyDelta: rec.WarmKeyDelta,
		}))
	}
}

// TestGossip_V2_CatchUpAcrossSealBoundary is the §1.5.2 contract test: a peer
// that missed live updates replays only the persisted diff journal (no gossip
// wire change). Under v2 composition the Finalizing→Settlement transition
// bulk-seals every live inference into sealed_acc; the follower must derive the
// same sealed_acc and state root as the leader without receiving sealed rows
// over gossip — sealed rows are re-inserted during replay via
// RebuildSealedInferenceIndex after ApplyLocal matches stored state hashes.
func TestGossip_V2_CatchUpAcrossSealBoundary(t *testing.T) {
	const escrowID = "escrow-1"

	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	leaderStore := storage.NewMemory()
	require.NoError(t, leaderStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 10000,
	}))

	leader, err := NewStateMachine(escrowID, config, group, 10000, user.Address(), verifier, leaderStore)
	require.NoError(t, err)

	executorSlotIdx := uint64(1) % uint64(len(hosts))

	// Same shape as applyStartConfirmFinish + finalize deadline as
	// TestV2_FinalizeDeadlineDrainsLiveIntoSealedAcc (nonces 1..3 inference,
	// then finalize and empty ticks through settlement drain).
	applyDiffPersist(t, leader, leaderStore, user, escrowID, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], escrowID, 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	applyDiffPersist(t, leader, leaderStore, user, escrowID, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: uint32(executorSlotIdx),
		EscrowId: escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlotIdx], finishMsg)
	applyDiffPersist(t, leader, leaderStore, user, escrowID, []*types.DevshardTx{txFinish(finishMsg)})

	applyDiffPersist(t, leader, leaderStore, user, escrowID, []*types.DevshardTx{txFinalize()})
	require.Equal(t, types.PhaseFinalizing, leader.Phase())

	st := leader.SnapshotState()
	for n := st.LatestNonce + 1; n <= st.FinalizeNonce+uint64(len(hosts)); n++ {
		applyDiffPersist(t, leader, leaderStore, user, escrowID, nil)
	}
	leaderFinal := leader.SnapshotState()
	require.Equal(t, types.PhaseSettlement, leaderFinal.Phase)
	require.Empty(t, leaderFinal.Inferences, "leader must drain live map at settlement")
	require.Len(t, leaderFinal.SealedAcc, 32)

	leaderRoot, err := leader.ComputeStateRoot()
	require.NoError(t, err)

	meta, err := leaderStore.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Greater(t, meta.LatestNonce, uint64(0))

	// Follower: fresh storage with only the diff journal (no sealed rows yet),
	// mirroring a peer that will catch up purely from GetDiffs + ApplyLocal.
	followerStore := storage.NewMemory()
	require.NoError(t, followerStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 10000,
	}))
	copyDiffJournal(t, leaderStore, followerStore, escrowID, 1, meta.LatestNonce)

	follower, err := NewStateMachine(escrowID, config, group, 10000, user.Address(), verifier, followerStore)
	require.NoError(t, err)

	records, err := followerStore.GetDiffs(escrowID, 1, meta.LatestNonce)
	require.NoError(t, err)
	for _, rec := range records {
		follower.InjectWarmKeys(rec.WarmKeyDelta)
		root, applyErr := follower.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, applyErr)
		if len(rec.StateHash) > 0 && len(root) > 0 {
			require.Equal(t, rec.StateHash, root, "replay must match leader state hash at nonce %d", rec.Nonce)
		}
	}
	require.NoError(t, follower.RebuildSealedInferenceIndex())

	followerFinal := follower.SnapshotState()
	require.Equal(t, types.PhaseSettlement, followerFinal.Phase)
	require.Empty(t, followerFinal.Inferences)
	require.Equal(t, leaderFinal.SealedAcc, followerFinal.SealedAcc,
		"follower must reproduce leader sealed_acc across settlement drain")

	followerRoot, err := follower.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, leaderRoot, followerRoot)

	_, ok, err := followerStore.GetSealedInference(escrowID, 1)
	require.NoError(t, err)
	require.True(t, ok, "follower storage must have sealed row after replay + rebuild")
}
