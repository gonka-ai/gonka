package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func keepertestInference(t *testing.T) (keeper.Keeper, sdk.Context) {
	t.Helper()
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	return k, ctx
}

func TestEvaluateSealedSegment_SkipsWhenCommitsExistButNoVotes(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
		}},
	}))
	require.NoError(t, k.PoCChallenge.SetCommit(ctx, types.PoCChallengeCommit{
		Target:                   testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "m1",
		SliceIndex:               0,
		Count:                    10,
		RootHash:                 make([]byte, 32),
	}))
	require.NoError(t, am.EvaluateSealedSegment(ctx, testutil.Executor, 100, types.PoCValidationSnapshot{}))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET, ch.FailReason)
	require.Equal(t, types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING, ch.Segments[0].Outcome)
}

func TestEvaluateSealedSegment_MissingCommitZeroReading(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			ConfirmationWeight: 100,
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
			FirstVoteHeight:          801,
		}},
	}))
	require.NoError(t, am.EvaluateSealedSegment(ctx, testutil.Executor, 100, types.PoCValidationSnapshot{}))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, ch.FailReason)
	root, ok := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, ok)
	require.Equal(t, int64(0), root.ValidationWeights[0].ConfirmationWeight)
}

func TestEvaluateSealedSegment_DoesNotOverwriteFailReason(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:          2,
		Target:              testutil.Executor,
		FailReason:          types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL,
		GenerationEndHeight: 50,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
			FirstVoteHeight:          801,
		}},
	}))
	require.NoError(t, am.EvaluateSealedSegment(ctx, testutil.Executor, 100, types.PoCValidationSnapshot{}))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL, ch.FailReason)
}

func TestFinalizeOpenChallenges_FailsMissingVoteAfterCommits(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	ctx = ctx.WithBlockHeight(900)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
		}},
	}))
	require.NoError(t, k.PoCChallenge.SetCommit(ctx, types.PoCChallengeCommit{
		Target:                   testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "m1",
		SliceIndex:               0,
		Count:                    10,
		RootHash:                 make([]byte, 32),
	}))
	require.NoError(t, am.FinalizeOpenChallenges(ctx, 2))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE, ch.FailReason)
}

func TestFinalizeOpenChallenges_MissingCommitWhenDark(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			ConfirmationWeight: 100,
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex: 2,
		Target:     testutil.Executor,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
		}},
	}))
	require.NoError(t, am.FinalizeOpenChallenges(ctx, 2))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, ch.FailReason)
	root, ok := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, ok)
	require.Equal(t, int64(0), root.ValidationWeights[0].ConfirmationWeight)
}

func TestFinalizeOpenChallenges_FailsWhenVoteWithoutSnapshot(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex: 2,
		Target:     testutil.Executor,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
			FirstVoteHeight:          801,
		}},
	}))
	require.NoError(t, k.PoCChallenge.SetCommit(ctx, types.PoCChallengeCommit{
		Target:                   testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "m1",
		SliceIndex:               0,
		Count:                    10,
		RootHash:                 make([]byte, 32),
	}))
	require.NoError(t, am.FinalizeOpenChallenges(ctx, 2))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE, ch.FailReason)
}

func TestSealOpenSegment_AutoPassesShortSegment(t *testing.T) {
	k, ctx := keepertestInference(t)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		Target: testutil.Executor,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
		}},
	}))
	require.NoError(t, pocchallenge.SealOpenSegment(ctx, k.PoCChallenge, testutil.Executor, 250, 500))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_AUTO_PASSED, ch.Segments[0].Outcome)
	require.Equal(t, int64(250), ch.Segments[0].SealHeight)
}

func TestSliceFailsAlphaUsesConfirmationPoC(t *testing.T) {
	params := types.DefaultConfirmationPoCParams()
	params.AlphaThreshold = types.DecimalFromFloat(0.5)
	require.True(t, sliceFailsAlpha(1, 100, params))
	require.False(t, sliceFailsAlpha(100, 100, params))
}

func TestAccumulateSegmentReading_UnderweightUsesFullExpected(t *testing.T) {
	params := types.DefaultConfirmationPoCParams()
	params.AlphaThreshold = types.DecimalFromFloat(0.5)
	counted := []pocchallenge.SliceRange{{Index: 0, Length: 500}, {Index: 1, Length: 300}}
	validated, expected, reason := accumulateSegmentReading(counted, 1, 1, params, func(sl pocchallenge.SliceRange) (bool, int64, bool) {
		if sl.Index == 0 {
			return true, 1, false
		}
		return true, sl.Length, false
	})
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT, reason)
	require.Equal(t, int64(800), expected)
	require.Equal(t, int64(301), validated)
}

func TestEvaluateSealedSegment_MissingAssignedModelCommit(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m2",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n2", PocWeight: 10}},
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
			FirstVoteHeight:          801,
		}},
	}))
	require.NoError(t, k.PoCChallenge.SetCommit(ctx, types.PoCChallengeCommit{
		Target:                   testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "m1",
		SliceIndex:               0,
		Count:                    10,
		RootHash:                 make([]byte, 32),
	}))
	require.NoError(t, am.EvaluateSealedSegment(ctx, testutil.Executor, 100, types.PoCValidationSnapshot{}))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, ch.FailReason)
}

func TestDecideVotedUsesLiveTriggerSnapshot(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
			FirstVoteHeight:          801,
		}},
	}))
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 2000,
		TotalNetworkWeight:  100,
	}))
	am.decideVotedChallengeSegments(ctx, 2000)
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, ch.FailReason)
}

func TestCatchUpSealUsesGenerationEndPlusOne(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex:            2,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		GenerationStartHeight: 80,
		TriggerHeight:         70,
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		Target: testutil.Executor,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 50,
		}},
	}))
	height := int64(91)
	ec := &types.EpochContext{EpochIndex: 2, PocStartBlockHeight: 0, EpochParams: *params.EpochParams}
	require.NoError(t, am.handleConfirmationPoCPhaseTransitions(ctx, height, ec, params))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	event := types.ConfirmationPoCEvent{GenerationStartHeight: 80}
	want := event.GetGenerationEnd(params.EpochParams) + 1
	require.Equal(t, want, ch.Segments[0].SealHeight)
	require.NotEqual(t, height, ch.Segments[0].SealHeight)
}

func TestChallengeVotingPowerKeepsSoleHostAndUsesRootWeight(t *testing.T) {
	target := testutil.Executor
	other := testutil.Validator
	vp, total := challengeVotingPower(target, types.PoCValidationSnapshot{
		TotalNetworkWeight: 150,
		ModelVotingPowers: []*types.ModelVotingPowers{
			{ModelId: "m1", VotingPowers: []*types.VotingPowerEntry{
				{Address: target, VotingPower: 40},
			}},
			{ModelId: "m2", VotingPowers: []*types.VotingPowerEntry{
				{Address: target, VotingPower: 10},
				{Address: other, VotingPower: 90},
			}},
		},
	}, 100)
	require.Equal(t, int64(50), total)
	require.Equal(t, int64(40), vp["m1"][target])
	_, ok := vp["m2"][target]
	require.False(t, ok)
	require.Equal(t, int64(90), vp["m2"][other])
}

func TestTargetTrustWeightPrefersCapAppliedWeight(t *testing.T) {
	target := testutil.Executor
	root := []*types.ValidationWeight{{MemberAddress: target, Weight: 100}}
	require.Equal(t, int64(40), targetTrustWeight(target, root, []*types.ActiveParticipant{{
		Index:     target,
		Weight:    100,
		CapWeight: 40,
	}}, true))
	require.Equal(t, int64(100), targetTrustWeight(target, root, nil, false))
}

func TestFinalizeOpenChallenges_DecidesOlderEpochPending(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 1,
		ModelId:    "m1",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        100,
			MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			ConfirmationWeight: 100,
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex: 1,
		Target:     testutil.Executor,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
			SealHeight:               800,
		}},
	}))
	require.NoError(t, am.FinalizeOpenChallenges(ctx, 2))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, ch.FailReason)
}

func TestLateCompletedUsesBlockHeightAsNextSegmentStart(t *testing.T) {
	k, ctx := keepertestInference(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.EpochParams.ConfirmationPocSafetyWindow = 50
	require.NoError(t, k.SetParams(ctx, params))

	event := types.ConfirmationPoCEvent{
		EpochIndex:            2,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
		GenerationStartHeight: 80,
		TriggerHeight:         70,
	}
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, event))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 50,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 50,
			SealHeight:               89,
			Outcome:                  types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PASSED,
		}},
	}))

	vePlusOne := event.GetValidationEnd(params.EpochParams) + 1
	height := vePlusOne + 20
	require.Greater(t, height, vePlusOne)

	ec := &types.EpochContext{EpochIndex: 2, PocStartBlockHeight: 0, EpochParams: *params.EpochParams}
	require.NoError(t, am.handleConfirmationPoCPhaseTransitions(ctx, height, ec, params))

	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	seg := k.PoCChallenge.SegmentByStart(ch, height)
	require.NotNil(t, seg)

	ctx = ctx.WithBlockHeight(height)
	require.NoError(t, pocchallenge.HandleEndBlock(ctx, &k, k.PoCChallenge))
	ch, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	seg = k.PoCChallenge.SegmentByStart(ch, height)
	require.NotNil(t, seg)
	require.NotEmpty(t, seg.SeedHash)
}
