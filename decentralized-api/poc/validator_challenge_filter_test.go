package poc

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestFilterChallengedConfirmationCommits(t *testing.T) {
	honest := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1honest", ModelId: "model"}
	challenged := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1challenged", ModelId: "model"}
	otherEpoch := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1other", ModelId: "model"}
	passed := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1passed", ModelId: "model"}

	open := openCh(challenged.ParticipantAddress, 100, 200, false)
	open.Challenge.EpochIndex = 7
	stale := openCh(otherEpoch.ParticipantAddress, 100, 200, false)
	stale.Challenge.EpochIndex = 6
	closed := openCh(passed.ParticipantAddress, 100, 200, false)
	closed.Challenge.EpochIndex = 7
	closed.Challenge.State = types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED

	filtered, skipped := filterChallengedConfirmationCommits(
		[]*types.PoCV2StoreCommitWithAddress{honest, challenged, otherEpoch, passed},
		7,
		[]*types.OpenPoCChallenge{open, stale, closed},
	)

	require.Equal(t, 1, skipped)
	require.Equal(t, []*types.PoCV2StoreCommitWithAddress{honest, otherEpoch, passed}, filtered)
}

func TestFilterChallengedConfirmationCommitsLeavesUnchallengedInputUnchanged(t *testing.T) {
	commits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "gonka1honest", ModelId: "model"},
	}

	filtered, skipped := filterChallengedConfirmationCommits(commits, 7, nil)

	require.Zero(t, skipped)
	require.Equal(t, commits, filtered)
}
