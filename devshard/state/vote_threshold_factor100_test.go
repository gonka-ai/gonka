package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// factor=100 passes DevshardEscrowParams.Validate ((0,100]) but makes
// VoteThreshold == groupSize, and votes pass only with weight > threshold:
// a unanimous timeout vote is still rejected.
func TestVoteThresholdFactor100_UnanimousTimeoutRejected(t *testing.T) {
	const groupSize = 6
	hosts := make([]*signing.Secp256k1Signer, groupSize)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	cfg := types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{VoteThresholdFactor: 100})
	require.Equal(t, uint32(groupSize), cfg.VoteThreshold)

	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", cfg, group, 100_000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100_000))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("p"), Model: "m",
		InputLength: 10, MaxTokens: testutil.TestMaxTokens, StartedAt: 1,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	votes := make([]*types.TimeoutVote, groupSize)
	for i := uint32(0); i < groupSize; i++ {
		v := testutil.SignTimeoutVote(t, hosts[i], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = i
		votes[i] = v
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientVotes)
	t.Logf("all %d slots accept, threshold %d: %v", groupSize, cfg.VoteThreshold, err)
}
