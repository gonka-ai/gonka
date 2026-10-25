package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestApplyDiff_Timeout_SkewedSlotsReachableByNonExecutorMajority(t *testing.T) {
	// 16-slot group skewed [13, 2, 1] as reported in #1570:
	// VoteThreshold = 16/2 = 8, while non-executor weight is 2+1 = 3 <= 8.
	// Effective non-executor threshold is 3/2 = 1 (need >=2 non-executor weight).
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), // 13 slots (0..12)
		testutil.MustGenerateKey(t), // 2 slots (13..14)
		testutil.MustGenerateKey(t), // 1 slot (15)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{13, 2, 1})
	config := testutil.DefaultConfig(len(group))
	require.Equal(t, uint32(8), config.VoteThreshold)

	sm, err := NewStateMachine(
		"escrow-skew",
		config,
		group,
		10000,
		user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-skew", user.Address(), config, group, 10000),
	)
	require.NoError(t, err)

	// Nonce 1 lands on slot 1 (owned by signers[0], 13 slots).
	diff := testutil.SignDiff(t, user, "escrow-skew", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, uint32(8), sm.VoteThreshold(), "frozen session VoteThreshold must remain unchanged")
	require.Equal(t, uint32(1), sm.TimeoutVoteThresholdForInference(1))

	// A single 1-slot verifier (signers[2], weight=1) is not a strict majority of 3 -> rejected.
	vote1 := testutil.SignTimeoutVote(t, signers[2], "escrow-skew", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	vote1.VoterSlot = 15
	makeTimeoutDue(t, sm, 1)
	diff = testutil.SignDiff(t, user, "escrow-skew", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Votes: []*types.TimeoutVote{vote1},
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientVotes)

	// Both non-executor verifiers accept (2 + 1 = 3 > 1) -> resolves without signers[0].
	vote2 := testutil.SignTimeoutVote(t, signers[1], "escrow-skew", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	vote2.VoterSlot = 13
	makeTimeoutDue(t, sm, 1)
	diff = testutil.SignDiff(t, user, "escrow-skew", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Votes: []*types.TimeoutVote{vote1, vote2},
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.StatusTimedOut, sm.SnapshotState().Inferences[1].Status)

	// Nonce 15 lands on slot 15 (signers[2], 1 slot): non-executor weight is 15 > 8,
	// so the full group VoteThreshold (8) still applies.
	require.Equal(t, uint32(8), sm.TimeoutVoteThresholdForInference(15))
}
