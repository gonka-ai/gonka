package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestProposerSig_VoteCannotAuthenticateValidation(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirmFinish(t, sm, user, hosts, 1)
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[1].Status)

	vote := &types.MsgValidationVote{
		InferenceId: 1, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1",
	}
	vote.ProposerSig = testutil.SignProposerTx(t, hosts[2], vote)
	retag := &types.MsgValidation{
		InferenceId:   vote.InferenceId,
		ValidatorSlot: vote.VoterSlot,
		Valid:         vote.VoteValid,
		EscrowId:      vote.EscrowId,
		ProposerSig:   vote.ProposerSig,
	}
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(retag)})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
	require.False(t, sm.SnapshotState().Inferences[1].ValidatedBy.IsSet(2))
}

func TestRetaggedValidationDoesNotShadowVote(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirmFinish(t, sm, user, hosts, 1)

	challenge := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	challenge.ProposerSig = testutil.SignProposerTx(t, hosts[0], challenge)
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(challenge)}))
	require.NoError(t, err)

	vote := &types.MsgValidationVote{
		InferenceId: 1, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1",
	}
	vote.ProposerSig = testutil.SignProposerTx(t, hosts[2], vote)
	retag := &types.MsgValidation{
		InferenceId:   vote.InferenceId,
		ValidatorSlot: vote.VoterSlot,
		Valid:         vote.VoteValid,
		EscrowId:      vote.EscrowId,
		ProposerSig:   append([]byte(nil), vote.ProposerSig...),
	}

	// Composed retag-first: the retag must not consume the voter's bitmap slot
	// and leave the real vote to fail as a duplicate.
	nonce++
	_, applied, err := sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{txValidation(retag), txVote(vote)})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.NotNil(t, applied[0].GetValidationVote())

	rec := sm.SnapshotState().Inferences[1]
	require.True(t, rec.ValidatedBy.IsSet(2), "honest vote must land")
	require.Greater(t, rec.VotesInvalid, uint32(1), "vote weight must count; retag must not occupy the bitmap")
}
