package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// newSealFoldSM builds a state machine bound to a caller-supplied user key so
// several machines (user, host, replay) can share identical inputs and verify
// the same user-signed diffs.
func newSealFoldSM(t *testing.T, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, balance uint64) *StateMachine {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance)
	sm, err := NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier, store)
	require.NoError(t, err)
	return sm
}

// buildStartConfirmFinishDiffs returns the three user-signed diffs that bring
// inferenceID through Pending -> Started -> Finished, starting at startNonce.
// The same signed diffs can be applied to multiple machines because they carry
// no machine-local state.
func buildStartConfirmFinishDiffs(t *testing.T, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, inferenceID, startNonce uint64) []types.Diff {
	t.Helper()
	executorSlotIdx := inferenceID % uint64(len(hosts))

	start := testutil.SignDiff(t, user, "escrow-1", startNonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})

	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	confirm := testutil.SignDiff(t, user, "escrow-1", startNonce+1, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})

	finishMsg := &types.MsgFinishInference{
		InferenceId: inferenceID, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: uint32(executorSlotIdx),
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlotIdx], finishMsg)
	finish := testutil.SignDiff(t, user, "escrow-1", startNonce+2, []*types.DevshardTx{txFinish(finishMsg)})

	return []types.Diff{start, confirm, finish}
}

// TestV2_DiffSeal_UserHostReplayAgree is the core no-divergence guarantee: once
// the seal decision is frozen into a signed diff, the user (composing), the host
// (applying+verifying), and a fresh replay all derive the identical state root
// and sealed_acc. The fold takes no wall-clock input, so clock skew between the
// sequencer and any host cannot move the root -- which is exactly the
// divergence the wall-clock seal gate used to cause.
func TestV2_DiffSeal_UserHostReplayAgree(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)

	userSM := newSealFoldSM(t, hosts, user, 1_000_000)
	hostSM := newSealFoldSM(t, hosts, user, 1_000_000)
	replaySM := newSealFoldSM(t, hosts, user, 1_000_000)

	var history []types.Diff
	applyUserHost := func(d types.Diff) {
		userRoot, err := userSM.ApplyDiff(d)
		require.NoError(t, err)
		hostRoot, err := hostSM.ApplyDiff(d)
		require.NoError(t, err)
		require.Equal(t, userRoot, hostRoot, "user and host roots must match at nonce %d", d.Nonce)
		history = append(history, d)
	}

	for _, d := range buildStartConfirmFinishDiffs(t, user, hosts, 1, 1) {
		applyUserHost(d)
	}
	require.Contains(t, userSM.SnapshotState().Inferences, uint64(1), "inference must be live before seal")

	// The sequencer freezes the seal of inference 1 into the next diff: it
	// computes the post_state_root locally (best-effort), then signs over the
	// txs + seal set + root.
	sealNonce := userSM.LatestNonce() + 1
	userRoot, applied, err := userSM.ApplyLocalBestEffort(sealNonce, nil, []uint64{1})
	require.NoError(t, err)
	require.Empty(t, applied, "seal-only diff applies no txs")

	sealDiff := testutil.SignDiffSealedWithRoot(t, user, "escrow-1", sealNonce, nil, []uint64{1}, userRoot)

	// The host applies the signed diff. ApplyDiff verifies the signature AND
	// asserts its own recomputed root equals the signed post_state_root, so a
	// successful apply already proves user==host. We assert the returned root
	// too for an explicit equality signal.
	hostRoot, err := hostSM.ApplyDiff(sealDiff)
	require.NoError(t, err)
	require.Equal(t, userRoot, hostRoot)
	history = append(history, sealDiff)

	// Replay reconstructs sealed_acc purely from the persisted seal set.
	for _, d := range history {
		_, err := replaySM.ApplyLocalSealed(d.Nonce, d.Txs, d.SealedInferenceIDs)
		require.NoError(t, err)
	}
	replayRoot, err := replaySM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, userRoot, replayRoot, "replay root must match user/host")

	// Inference 1 is sealed everywhere: dropped from live state, folded into a
	// non-zero, identical sealed_acc.
	var zero [32]byte
	userAcc := userSM.SnapshotState().SealedAcc
	require.Len(t, userAcc, 32)
	require.NotEqual(t, zero[:], userAcc, "sealed_acc must change after folding the seal")
	require.Equal(t, userAcc, hostSM.SnapshotState().SealedAcc)
	require.Equal(t, userAcc, replaySM.SnapshotState().SealedAcc)
	for name, sm := range map[string]*StateMachine{"user": userSM, "host": hostSM, "replay": replaySM} {
		require.NotContains(t, sm.SnapshotState().Inferences, uint64(1), "%s must drop sealed inference from live state", name)
	}
}

// TestV2_DiffSeal_FoldOrderInvariant verifies the fold is invariant to the
// order and multiplicity of the proposed seal ids: foldSealedIDsLocked sorts and
// deduplicates, so a sequencer that lists ids in any order (or repeats one)
// yields the identical sealed_acc and root. This protects against a subtle
// divergence where two sequencers emit the same logical seal set in different
// list orders.
func TestV2_DiffSeal_FoldOrderInvariant(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)

	smA := newSealFoldSM(t, hosts, user, 1_000_000)
	smB := newSealFoldSM(t, hosts, user, 1_000_000)

	driveBoth := func(inferenceID, startNonce uint64) {
		for _, d := range buildStartConfirmFinishDiffs(t, user, hosts, inferenceID, startNonce) {
			_, err := smA.ApplyDiff(d)
			require.NoError(t, err)
			_, err = smB.ApplyDiff(d)
			require.NoError(t, err)
		}
	}
	// Inference 1 uses nonces 1..3, inference 4 uses nonces 4..6 (the start tx
	// requires inference_id == nonce).
	driveBoth(1, 1)
	driveBoth(4, 4)

	sealNonce := smA.LatestNonce() + 1
	rootA, _, err := smA.ApplyLocalBestEffort(sealNonce, nil, []uint64{1, 4})
	require.NoError(t, err)
	rootB, _, err := smB.ApplyLocalBestEffort(sealNonce, nil, []uint64{4, 1, 1}) // unsorted + duplicate
	require.NoError(t, err)

	require.Equal(t, rootA, rootB, "fold must be invariant to seal-id order and duplicates")
	require.Equal(t, smA.SnapshotState().SealedAcc, smB.SnapshotState().SealedAcc)
	require.Empty(t, smA.SnapshotState().Inferences)
	require.Empty(t, smB.SnapshotState().Inferences)
}

// TestV2_DiffSeal_PostStateRootCommitsToSeal proves the seal is bound into the
// signed post_state_root: a diff that claims to seal an inference but commits to
// the root of a state that did NOT seal it is rejected, and the rejection rolls
// back everything (nonce and seal). This is the mechanism that stops a divergent
// or dishonest seal decision from being applied+signed.
func TestV2_DiffSeal_PostStateRootCommitsToSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)

	hostSM := newSealFoldSM(t, hosts, user, 1_000_000)
	refSM := newSealFoldSM(t, hosts, user, 1_000_000)

	for _, d := range buildStartConfirmFinishDiffs(t, user, hosts, 1, 1) {
		_, err := hostSM.ApplyDiff(d)
		require.NoError(t, err)
		_, err = refSM.ApplyDiff(d)
		require.NoError(t, err)
	}

	sealNonce := hostSM.LatestNonce() + 1
	// The root of a state that advances the nonce but does NOT seal inference 1.
	noSealRoot, _, err := refSM.ApplyLocalBestEffort(sealNonce, nil, nil)
	require.NoError(t, err)

	// A diff that claims to seal [1] but commits to the no-seal root: the host
	// folds the seal, recomputes a different root, and must reject the diff.
	badDiff := testutil.SignDiffSealedWithRoot(t, user, "escrow-1", sealNonce, nil, []uint64{1}, noSealRoot)
	_, err = hostSM.ApplyDiff(badDiff)
	require.ErrorIs(t, err, types.ErrPostStateRootMismatch)

	// Full rollback: inference still live, nonce not advanced.
	require.Contains(t, hostSM.SnapshotState().Inferences, uint64(1), "rejected seal must not drop the inference")
	require.Equal(t, sealNonce-1, hostSM.LatestNonce(), "rejected seal must not advance the nonce")
}
