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
	failed := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1failed", ModelId: "model"}
	aborted := &types.PoCV2StoreCommitWithAddress{ParticipantAddress: "gonka1aborted", ModelId: "model"}

	open := openCh(challenged.ParticipantAddress, 100, 200, false)
	open.Challenge.EpochIndex = 7
	stale := openCh(otherEpoch.ParticipantAddress, 100, 200, false)
	stale.Challenge.EpochIndex = 6
	closedPassed := openCh(passed.ParticipantAddress, 100, 200, false)
	closedPassed.Challenge.EpochIndex = 7
	closedPassed.Challenge.State = types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED
	closedFailed := openCh(failed.ParticipantAddress, 100, 200, false)
	closedFailed.Challenge.EpochIndex = 7
	closedFailed.Challenge.State = types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED
	closedAborted := openCh(aborted.ParticipantAddress, 100, 200, false)
	closedAborted.Challenge.EpochIndex = 7
	closedAborted.Challenge.State = types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED

	cache := NewChallengeCache()
	cache.Replace("", []*types.OpenPoCChallenge{open, stale, closedPassed, closedFailed, closedAborted}, 0)

	filtered, skipped := filterChallengedConfirmationCommits(
		[]*types.PoCV2StoreCommitWithAddress{honest, challenged, otherEpoch, passed, failed, aborted},
		cache.TargetsForEpoch(7),
	)

	require.Equal(t, 4, skipped)
	require.Equal(t, []*types.PoCV2StoreCommitWithAddress{honest, otherEpoch}, filtered)
}

func TestFilterChallengedConfirmationCommitsLeavesUnchallengedInputUnchanged(t *testing.T) {
	commits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "gonka1honest", ModelId: "model"},
	}

	filtered, skipped := filterChallengedConfirmationCommits(commits, nil)

	require.Zero(t, skipped)
	require.Equal(t, commits, filtered)
}
