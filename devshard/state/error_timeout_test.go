package state

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const errorTimeoutResponseHash = "response"

func errorTimeoutHosts(t *testing.T) []*signing.Secp256k1Signer {
	t.Helper()
	return []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
}

func applyStartConfirm(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, inferenceID uint64) uint32 {
	t.Helper()
	executorSlot := uint32(inferenceID % uint64(len(hosts)))
	nonce := sm.SnapshotState().LatestNonce + 1

	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlot], "escrow-1", inferenceID, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	return executorSlot
}

func signedFinish(t *testing.T, hosts []*signing.Secp256k1Signer, inferenceID uint64, executorSlot uint32, inputTokens, outputTokens uint64, responseHash []byte) *types.MsgFinishInference {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: responseHash,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlot], msg)
	return msg
}

func errorTimeoutVotes(t *testing.T, hosts []*signing.Secp256k1Signer, inferenceID uint64, responseHash []byte, slots []uint32) []*types.ErrorMissVote {
	t.Helper()
	votes := make([]*types.ErrorMissVote, 0, len(slots))
	for _, slot := range slots {
		v := testutil.SignErrorMissVote(t, hosts[slot], "escrow-1", inferenceID, true, responseHash)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	return votes
}

func TestApplyDiff_Timeout_Error_SameDiff(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	pre := sm.SnapshotState()
	require.Equal(t, uint64(0), pre.HostStats[slot].Cost)
	reserved := uint64(100 + testutil.TestMaxTokens)
	require.Equal(t, uint64(10000)-reserved, pre.Balance)

	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 80, 40, hash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txFinish(finish),
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
	})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	rec := state.Inferences[1]
	require.Equal(t, types.StatusTimedOut, rec.Status)
	require.Equal(t, uint32(1), state.HostStats[slot].Missed)
	require.Equal(t, uint32(0), state.HostStats[slot].Invalid)
	require.Equal(t, pre.HostStats[slot].Cost, state.HostStats[slot].Cost)
	require.Equal(t, uint64(10000), state.Balance)
	require.Equal(t, reserved, rec.ReservedCost)
	require.Equal(t, uint64(120), rec.ActualCost)
}

func TestApplyDiff_Timeout_Error_RequiresFinished(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	hash := []byte(errorTimeoutResponseHash)

	t.Run("pending", func(t *testing.T) {
		sm, user := newTestSM(t, hosts, 10000)
		diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
			InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		})})
		_, err := sm.ApplyDiff(diff)
		require.NoError(t, err)
		before := sm.SnapshotState()

		votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
		diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		})})
		_, err = sm.ApplyDiff(diff)
		require.ErrorIs(t, err, types.ErrInvalidTransition)
		after := sm.SnapshotState()
		require.Equal(t, types.StatusPending, after.Inferences[1].Status)
		require.Equal(t, before.Balance, after.Balance)
		require.Equal(t, uint32(0), after.HostStats[1].Missed)
	})

	t.Run("started", func(t *testing.T) {
		sm, user := newTestSM(t, hosts, 10000)
		applyStartConfirm(t, sm, user, hosts, 1)
		before := sm.SnapshotState()

		votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
		nonce := sm.SnapshotState().LatestNonce + 1
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		})})
		_, err := sm.ApplyDiff(diff)
		require.ErrorIs(t, err, types.ErrInvalidTransition)
		after := sm.SnapshotState()
		require.Equal(t, types.StatusStarted, after.Inferences[1].Status)
		require.Equal(t, before.Balance, after.Balance)
		require.Equal(t, uint32(0), after.HostStats[1].Missed)
	})
}

func TestApplyDiff_Timeout_Error_InsufficientVotes(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 80, 40, hash)
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finish)}))
	require.NoError(t, err)
	before := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, before.Inferences[1].Status)
	require.Equal(t, uint64(120), before.HostStats[slot].Cost)

	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2})
	nonce++
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txErrorMiss(&types.MsgErrorMiss{
		InferenceId: 1, Votes: votes,
	})}))
	require.ErrorIs(t, err, types.ErrInsufficientVotes)
	after := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, after.Inferences[1].Status)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, before.HostStats[slot].Cost, after.HostStats[slot].Cost)
	require.Equal(t, uint32(0), after.HostStats[slot].Missed)
}

func TestApplyDiff_Timeout_Error_ZeroTokenFinish(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	pre := sm.SnapshotState()
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 0, 0, hash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txFinish(finish),
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
	}))
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, state.Inferences[1].Status)
	require.Equal(t, uint32(1), state.HostStats[slot].Missed)
	require.Equal(t, pre.HostStats[slot].Cost, state.HostStats[slot].Cost)
	require.Equal(t, uint64(10000), state.Balance)
	require.Equal(t, uint64(0), state.Inferences[1].ActualCost)
}

func TestApplyDiff_Timeout_Error_ActualCostCappedAtReserved(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	pre := sm.SnapshotState()
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 200, 200, hash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txFinish(finish),
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
	}))
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, state.Inferences[1].Status)
	require.Equal(t, uint64(100+testutil.TestMaxTokens), state.Inferences[1].ActualCost)
	require.Equal(t, uint32(1), state.HostStats[slot].Missed)
	require.Equal(t, pre.HostStats[slot].Cost, state.HostStats[slot].Cost)
	require.Equal(t, uint64(10000), state.Balance)
}

func TestApplyDiff_Timeout_Error_WrongOrderRejected(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	before := sm.SnapshotState()
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 80, 40, hash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
		txFinish(finish),
	}))
	require.ErrorIs(t, err, types.ErrInvalidTransition)
	after := sm.SnapshotState()
	require.Equal(t, types.StatusStarted, after.Inferences[1].Status)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, before.HostStats[slot].Cost, after.HostStats[slot].Cost)
	require.Equal(t, uint32(0), after.HostStats[slot].Missed)
}

func TestApplyDiff_Timeout_Error_SealedFinishRejected(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirmFinish(t, sm, user, hosts, 1)
	require.NoError(t, sm.SealInference(1))
	before := sm.SnapshotState()

	hash := []byte(errorTimeoutResponseHash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txErrorMiss(&types.MsgErrorMiss{
		InferenceId: 1, Votes: votes,
	})}))
	require.ErrorIs(t, err, types.ErrInvalidTransition)
	require.Contains(t, err.Error(), "sealed")
	after := sm.SnapshotState()
	require.Nil(t, after.Inferences[1])
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, before.HostStats[1].Missed, after.HostStats[1].Missed)
	require.Equal(t, before.HostStats[1].Cost, after.HostStats[1].Cost)
}

func TestApplyDiff_Timeout_Error_PostTimeoutValidationRejected(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 80, 40, hash)
	votes := errorTimeoutVotes(t, hosts, 1, hash, []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txFinish(finish),
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
	}))
	require.NoError(t, err)

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce = sm.SnapshotState().LatestNonce + 1
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)}))
	require.ErrorIs(t, err, types.ErrInvalidTransition)
	require.Equal(t, types.StatusTimedOut, sm.SnapshotState().Inferences[1].Status)

	voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 2, VoteValid: true, EscrowId: "escrow-1"}
	voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], voteMsg)
	nonce = sm.SnapshotState().LatestNonce + 1
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txVote(voteMsg)}))
	require.ErrorIs(t, err, types.ErrInvalidTransition)
	require.Equal(t, types.StatusTimedOut, sm.SnapshotState().Inferences[1].Status)
}

func TestApplyDiff_Timeout_Error_HashMismatchRejected(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	slot := applyStartConfirm(t, sm, user, hosts, 1)
	hash := []byte(errorTimeoutResponseHash)
	finish := signedFinish(t, hosts, 1, slot, 80, 40, hash)
	votes := errorTimeoutVotes(t, hosts, 1, []byte("other-hash"), []uint32{0, 2, 3})
	nonce := sm.SnapshotState().LatestNonce + 1
	before := sm.SnapshotState()
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		txFinish(finish),
		txErrorMiss(&types.MsgErrorMiss{
			InferenceId: 1, Votes: votes,
		}),
	}))
	require.ErrorIs(t, err, types.ErrInvalidVoteSig)
	after := sm.SnapshotState()
	require.Equal(t, types.StatusStarted, after.Inferences[1].Status)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, uint32(0), after.HostStats[slot].Missed)
}

func TestApplyDiff_Timeout_Error_CrossInferenceVoteRejected(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	hashA := []byte("body-a")
	hashB := []byte("body-b")

	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})}))
	require.NoError(t, err)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 2, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})}))
	require.NoError(t, err)

	exec1 := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: exec1, ConfirmedAt: 1000,
	})}))
	require.NoError(t, err)
	exec2 := testutil.SignExecutorReceipt(t, hosts[2], "escrow-1", 2, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 2, ExecutorSig: exec2, ConfirmedAt: 1000,
	})}))
	require.NoError(t, err)

	finish1 := signedFinish(t, hosts, 1, 1, 80, 40, hashA)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{txFinish(finish1)}))
	require.NoError(t, err)
	finish2 := signedFinish(t, hosts, 2, 2, 80, 40, hashB)
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 6, []*types.DevshardTx{txFinish(finish2)}))
	require.NoError(t, err)
	before := sm.SnapshotState()

	harvested := errorTimeoutVotes(t, hosts, 1, hashA, []uint32{0, 3, 4})
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 7, []*types.DevshardTx{txErrorMiss(&types.MsgErrorMiss{
		InferenceId: 2, Votes: harvested,
	})}))
	require.ErrorIs(t, err, types.ErrInvalidVoteSig)
	after := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, after.Inferences[1].Status)
	require.Equal(t, types.StatusFinished, after.Inferences[2].Status)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, uint32(0), after.HostStats[2].Missed)
}

func TestVerifyFinishProposerSig(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 10000)

	msg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("h"),
		InputTokens: 1, OutputTokens: 0, ExecutorSlot: 0, EscrowId: "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[0], msg)
	require.NoError(t, sm.VerifyFinishProposerSig(msg))

	require.ErrorIs(t, sm.VerifyFinishProposerSig(nil), types.ErrInvalidProposerSig)

	wrong := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("h"),
		InputTokens: 1, OutputTokens: 0, ExecutorSlot: 0, EscrowId: "escrow-1",
	}
	wrong.ProposerSig = testutil.SignProposerTx(t, hosts[1], wrong)
	require.ErrorIs(t, sm.VerifyFinishProposerSig(wrong), types.ErrInvalidProposerSig)

	unknownSlot := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("h"),
		InputTokens: 1, OutputTokens: 0, ExecutorSlot: 99, EscrowId: "escrow-1",
	}
	unknownSlot.ProposerSig = testutil.SignProposerTx(t, hosts[0], unknownSlot)
	require.ErrorIs(t, sm.VerifyFinishProposerSig(unknownSlot), types.ErrSlotNotInGroup)
}

func TestVerifyFinishProposerSig_CachedWarmKeyDoesNotCallResolver(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	warm := testutil.MustGenerateKey(t)
	var calls int
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		calls++
		return warmAddr == warm.Address() && coldAddr == hosts[0].Address(), nil
	}
	sm, _ := newTestSMWithWarmKey(t, hosts, 100000, resolver)

	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("h"),
		InputTokens: 0, OutputTokens: 0, ExecutorSlot: 0, EscrowId: "escrow-1",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, warm, finish)

	require.NoError(t, sm.VerifyFinishProposerSig(finish))
	require.Equal(t, 1, calls, "first miss must consult the resolver")
	require.NoError(t, sm.VerifyFinishProposerSig(finish))
	require.Equal(t, 1, calls, "cached warm key must take the read-locked path")
}

func TestVerifyFinishProposerSig_ConcurrentWithApplyDiff(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	warm := testutil.MustGenerateKey(t)
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warm.Address() && coldAddr == hosts[0].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 100000, resolver)

	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("h"),
		InputTokens: 0, OutputTokens: 0, ExecutorSlot: 0, EscrowId: "escrow-1",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, warm, finish)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 40 {
			_ = sm.VerifyFinishProposerSig(finish)
			_ = sm.SnapshotState()
		}
	}()
	go func() {
		defer wg.Done()
		for range 8 {
			nonce := sm.LatestNonce() + 1
			diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
				InferenceId: nonce, PromptHash: []byte("prompt"), Model: "llama",
				InputLength: 1, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
			})})
			_, err := sm.ApplyDiff(diff)
			require.NoError(t, err)
		}
	}()
	wg.Wait()
}

func TestInference_ReturnsCopyNotAlias(t *testing.T) {
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirm(t, sm, user, hosts, 1)

	rec, ok := sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	rec.Status = types.StatusTimedOut
	rec.ResponseHash = []byte("mutated")

	live, ok := sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, live.Status)
	require.NotEqual(t, []byte("mutated"), live.ResponseHash)

	_, ok = sm.Inference(99)
	require.False(t, ok)
}
