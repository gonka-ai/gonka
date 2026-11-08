package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func newChallengeSM(t *testing.T) (*StateMachine, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirmFinish(t, sm, user, hosts, 1)
	return sm, user, hosts
}

func applyChallengeTxs(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, txs ...*types.DevshardTx) error {
	t.Helper()
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
	return err
}

func initialValidation(t *testing.T, hosts []*signing.Secp256k1Signer, slot uint32, valid bool) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: slot, Valid: valid, EscrowId: "escrow-1"}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], msg)
	return txValidation(msg)
}

func challengeVote(t *testing.T, hosts []*signing.Secp256k1Signer, slot uint32, valid bool) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: valid, EscrowId: "escrow-1"}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], msg)
	return txVote(msg)
}

func TestLateInitialValidationCountsTowardTheChallenge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sameDiff   bool
		lateValid  bool
		votes      map[uint32]bool
		wantStatus types.InferenceStatus
	}{
		{name: "late invalid in a later diff tips the challenge", lateValid: false, votes: map[uint32]bool{3: false}, wantStatus: types.StatusInvalidated},
		{name: "late invalid in the same diff tips the challenge", sameDiff: true, lateValid: false, votes: map[uint32]bool{3: false}, wantStatus: types.StatusInvalidated},
		{name: "late valid counts for the executor", lateValid: true, votes: map[uint32]bool{3: true, 4: true}, wantStatus: types.StatusValidated},
		{name: "one more invalid vote is not enough without the late weight", lateValid: true, votes: map[uint32]bool{3: false}, wantStatus: types.StatusChallenged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm, user, hosts := newChallengeSM(t)
			first := initialValidation(t, hosts, 0, false)
			late := initialValidation(t, hosts, 2, tc.lateValid)
			if tc.sameDiff {
				require.NoError(t, applyChallengeTxs(t, sm, user, first, late))
			} else {
				require.NoError(t, applyChallengeTxs(t, sm, user, first))
				require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[1].Status)
				require.NoError(t, applyChallengeTxs(t, sm, user, late))
			}
			rec := sm.SnapshotState().Inferences[1]
			if tc.lateValid {
				require.Equal(t, uint32(1), rec.VotesValid)
				require.Equal(t, uint32(1), rec.VotesInvalid)
			} else {
				require.Equal(t, uint32(2), rec.VotesInvalid)
			}

			var votes []*types.DevshardTx
			for slot, valid := range tc.votes {
				votes = append(votes, challengeVote(t, hosts, slot, valid))
			}
			require.NoError(t, applyChallengeTxs(t, sm, user, votes...))

			require.Equal(t, tc.wantStatus, sm.SnapshotState().Inferences[1].Status)
		})
	}
}

func TestLateInitialValidatorCannotVoteAgain(t *testing.T) {
	sm, user, hosts := newChallengeSM(t)
	require.NoError(t, applyChallengeTxs(t, sm, user, initialValidation(t, hosts, 0, false)))
	require.NoError(t, applyChallengeTxs(t, sm, user, initialValidation(t, hosts, 2, false)))

	require.ErrorIs(t, applyChallengeTxs(t, sm, user, challengeVote(t, hosts, 2, false)), types.ErrDuplicateVote)
	require.Equal(t, uint32(2), sm.SnapshotState().Inferences[1].VotesInvalid)
}

func TestLateInitialValidationAfterResolutionIsNotCounted(t *testing.T) {
	sm, user, hosts := newChallengeSM(t)
	require.NoError(t, applyChallengeTxs(t, sm, user, initialValidation(t, hosts, 0, false)))
	require.NoError(t, applyChallengeTxs(t, sm, user, challengeVote(t, hosts, 2, false), challengeVote(t, hosts, 3, false)))
	resolved := sm.SnapshotState().Inferences[1]
	require.Equal(t, types.StatusInvalidated, resolved.Status)

	require.NoError(t, applyChallengeTxs(t, sm, user, initialValidation(t, hosts, 4, true)))

	rec := sm.SnapshotState().Inferences[1]
	require.Equal(t, types.StatusInvalidated, rec.Status)
	require.Equal(t, resolved.VotesValid, rec.VotesValid)
	require.Equal(t, resolved.VotesInvalid, rec.VotesInvalid)
	require.Equal(t, uint32(1), sm.SnapshotState().HostStats[1].Invalid)
}
