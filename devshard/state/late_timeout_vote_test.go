package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// A vote is never too late to apply. See cmd/gateway/docs/race.md, "The timeout-vote queue".
func TestATimeoutVoteIsNeverTooLateToApply(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		reason  types.TimeoutReason
		confirm bool
		status  types.InferenceStatus
	}{
		{name: "refused", reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, status: types.StatusTimedOut},
		{name: "execution", reason: types.TimeoutReason_TIMEOUT_REASON_EXECUTION, confirm: true, status: types.StatusTimedOut},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			hosts := []*signing.Secp256k1Signer{
				testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
				testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
			}
			sm, user := newTestSM(t, hosts, 10000)
			nonce := uint64(1)

			diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
				InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1,
			})})
			_, err := sm.ApplyDiff(diff)
			require.NoError(t, err)

			if testCase.confirm {
				nonce++
				receipt := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama",
					100, testutil.TestMaxTokens, 1, 1)
				diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
					InferenceId: 1, ExecutorSig: receipt, ConfirmedAt: 1,
				})})
				_, err = sm.ApplyDiff(diff)
				require.NoError(t, err)
			}

			var votes []*types.TimeoutVote
			for _, slot := range []uint32{0, 2, 3} {
				vote := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, testCase.reason, true)
				vote.VoterSlot = slot
				votes = append(votes, vote)
			}
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
				InferenceId: 1, Reason: testCase.reason, Votes: votes,
			})})

			_, err = sm.ApplyDiff(diff)

			require.NoError(t, err, "a vote posted long after its deadline must still apply")
			require.Equal(t, testCase.status, sm.SnapshotState().Inferences[1].Status)
		})
	}
}
