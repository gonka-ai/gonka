package state_test

import (
	"context"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

type auditPeer struct {
	txs     []*types.DevshardTx
	receipt []byte
}

func (p auditPeer) GetMempool(context.Context) ([]*types.DevshardTx, error) { return p.txs, nil }
func (p auditPeer) ChallengeReceipt(context.Context, uint64, *host.InferencePayload, []types.Diff) ([]byte, []*types.DevshardTx, error) {
	return p.receipt, p.txs, nil
}
func auditSetup(t *testing.T) (*state.StateMachine, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	hs := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warm := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	cfg := testutil.DefaultConfig(3)
	group := testutil.MakeGroup(hs)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100000), state.WithWarmKeyResolver(func(w, c string) (bool, error) { return w == warm.Address(), nil }))
	require.NoError(t, err)
	return sm, hs, warm
}
func auditApply(t *testing.T, sm *state.StateMachine, txs ...*types.DevshardTx) {
	_, e := sm.ApplyLocal(sm.LatestNonce()+1, txs)
	require.NoError(t, e)
}
func auditStart(t *testing.T, sm *state.StateMachine, id uint64) {
	auditApply(t, sm, &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: id, PromptHash: testutil.TestPromptHash[:], Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}}})
}
func auditConfirm(t *testing.T, sm *state.StateMachine, id uint64, signer signing.Signer, h uint64) *types.MsgConfirmStart {
	var hash []byte
	if h > 0 {
		hash = []byte("block")
	}
	sig := testutil.SignExecutorReceipt(t, signer, "escrow-1", id, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000, testutil.ReceiptStamp{Height: h, Hash: hash})
	return &types.MsgConfirmStart{InferenceId: id, ExecutorSig: sig, ConfirmedAt: 1000, ObservedHeight: h, ObservedBlockHash: hash}
}
func auditCS(c *types.MsgConfirmStart) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: c}}
}
func auditFI(f *types.MsgFinishInference) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: f}}
}
func auditFinish(t *testing.T, id uint64, signer signing.Signer) *types.MsgFinishInference {
	f := &types.MsgFinishInference{InferenceId: id, ExecutorSlot: uint32(id % 3), EscrowId: "escrow-1", ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40}
	f.ProposerSig = testutil.SignProposerTx(t, signer, f)
	return f
}
func TestTimeoutEvidenceRejectsHeightRegression(t *testing.T) {
	for _, execution := range []bool{false, true} {
		name := "refused"
		if execution {
			name = "execution"
		}
		t.Run(name, func(t *testing.T) {
			sm, hs, _ := auditSetup(t)
			auditStart(t, sm, 1)
			auditApply(t, sm, auditCS(auditConfirm(t, sm, 1, hs[1], 100)))
			auditStart(t, sm, 3)
			var tx *types.DevshardTx
			var receipt []byte
			if execution {
				auditApply(t, sm, auditCS(auditConfirm(t, sm, 3, hs[0], 0)))
				f := auditFinish(t, 3, hs[0])
				f.ObservedHeight = 99
				f.ObservedBlockHash = []byte("block")
				f.ProposerSig = nil
				f.ProposerSig = testutil.SignProposerTx(t, hs[0], f)
				require.ErrorContains(t, sm.VerifyFinishInference(f), "height_regression")
				tx = auditFI(f)
			} else {
				c := auditConfirm(t, sm, 3, hs[0], 99)
				require.ErrorContains(t, sm.VerifyConfirmStart(c), "height_regression")
				tx = auditCS(c)
				receipt = c.ExecutorSig
			}
			pool := host.NewMempool()
			payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
			st := sm.SnapshotState()
			for _, remote := range []bool{false, true} {
				var local []*types.DevshardTx
				var peer host.ExecutorClient
				if remote {
					peer = auditPeer{[]*types.DevshardTx{tx}, receipt}
				} else {
					local = []*types.DevshardTx{tx}
				}
				var accept bool
				var err error
				if execution {
					accept, err = host.VerifyExecutionTimeout(context.Background(), st, 3, local, peer, sm, pool, st.Config, 100000)
				} else {
					accept, err = host.VerifyRefusedTimeout(context.Background(), st, 3, payload, local, peer, pool, sm, st.Config, 100000)
				}
				require.NoError(t, err)
				require.True(t, accept, "non-applicable evidence must not veto a timeout")
			}
			_, err := sm.ApplyLocal(sm.LatestNonce()+1, []*types.DevshardTx{tx})
			require.ErrorContains(t, err, "height_regression")
			_, _, err = sm.ApplyLocalBestEffort(sm.LatestNonce()+1, []*types.DevshardTx{tx})
			require.ErrorContains(t, err, "height_regression")
			require.Empty(t, host.VerifiedRecoveryTxsFor(st, 3, pool.Txs(), sm))

			// Honest timeout votes can now release the reservation before drain.
			reason := types.TimeoutReason_TIMEOUT_REASON_REFUSED
			if execution {
				reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
			}
			var votes []*types.TimeoutVote
			for _, slot := range []uint32{1, 2} {
				vote := testutil.SignTimeoutVote(t, hs[slot], "escrow-1", 3, reason, true)
				vote.VoterSlot = slot
				votes = append(votes, vote)
			}
			before := sm.SnapshotState()
			auditApply(t, sm, &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 3, Reason: reason, Votes: votes}}})
			after := sm.SnapshotState()
			require.Equal(t, types.StatusTimedOut, after.Inferences[3].Status)
			require.Equal(t, before.Balance+before.Inferences[3].ReservedCost, after.Balance)
			require.Zero(t, after.HostStats[0].Cost)
			require.EqualValues(t, 1, after.HostStats[0].Missed)

		})
	}
}
func TestTimeoutEvidenceVerificationDoesNotMutateState(t *testing.T) {
	for _, mode := range []string{"confirm", "finish", "candidate"} {
		t.Run(mode, func(t *testing.T) {
			sm, hs, warm := auditSetup(t)
			auditStart(t, sm, 1)
			if mode == "finish" {
				auditApply(t, sm, auditCS(auditConfirm(t, sm, 1, hs[1], 0)))
			}
			before := sm.SnapshotState()
			root, e := sm.ComputeStateRoot()
			require.NoError(t, e)
			if mode == "confirm" {
				require.NoError(t, sm.VerifyConfirmStart(auditConfirm(t, sm, 1, warm, 0)))
			} else {
				f := auditFinish(t, 1, warm)
				if mode == "finish" {
					require.NoError(t, sm.VerifyFinishInference(f))
				} else {
					require.NoError(t, sm.VerifyFinishCandidate(f))
				}
				require.NoError(t, sm.VerifyFinishProposerSig(f))
				require.Empty(t, sm.WarmKeys(), "successful verification must be read-only too")
				f.InputTokens = math.MaxUint64
				f.OutputTokens = 1
				f.ProposerSig = nil
				f.ProposerSig = testutil.SignProposerTx(t, warm, f)
				if mode == "finish" {
					require.Error(t, sm.VerifyFinishInference(f))
				} else {
					require.Error(t, sm.VerifyFinishCandidate(f))
				}
			}
			after := sm.SnapshotState()
			require.Equal(t, before.LatestNonce, after.LatestNonce)
			require.Empty(t, before.WarmKeys)
			require.Empty(t, after.WarmKeys)
			root2, e := sm.ComputeStateRoot()
			require.NoError(t, e)
			require.Equal(t, root, root2)
			require.Equal(t, before, after)
		})
	}
}
func TestBestEffortRejectedValidationDoesNotBindWarmKey(t *testing.T) {
	sm, hs, warm := auditSetup(t)
	auditStart(t, sm, 1)
	auditApply(t, sm, auditCS(auditConfirm(t, sm, 1, hs[1], 0)))
	auditApply(t, sm, auditFI(auditFinish(t, 1, hs[1])))
	v := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "wrong"}
	v.ProposerSig = testutil.SignProposerTx(t, warm, v)
	_, applied, err := sm.ApplyLocalBestEffort(sm.LatestNonce()+1, []*types.DevshardTx{{Tx: &types.DevshardTx_Validation{Validation: v}}})
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Empty(t, sm.WarmKeys())

}

// Insufficient votes fail after warm-key resolution. This exercises rollback
// even when cheap escrow and signature checks have already succeeded.
func TestBestEffortRejectedWarmVotesMatchAdvertisedDiff(t *testing.T) {
	sm, _, warm := auditSetup(t)
	auditStart(t, sm, 1)
	vote := testutil.SignTimeoutVote(t, warm, "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	vote.VoterSlot = 0
	tx := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: []*types.TimeoutVote{vote}}}}
	before := sm.SnapshotState()
	vd, err := sm.PreviewLocalBestEffort(sm.LatestNonce()+1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	require.Empty(t, vd.Applied)
	require.Empty(t, vd.WarmAfter)
	require.Equal(t, before, sm.SnapshotState())
	root, err := sm.ApplyLocal(sm.LatestNonce()+1, vd.Applied)
	require.NoError(t, err)
	require.Equal(t, root, vd.Root, "signed root must replay from exactly the advertised transactions")
	require.Empty(t, sm.WarmKeys())
}
