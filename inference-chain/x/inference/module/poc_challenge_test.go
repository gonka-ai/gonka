package inference

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func challengeApp(t *testing.T) (AppModule, keeper.Keeper, sdk.Context) {
	t.Helper()
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.EpochParams.ConfirmationPocSafetyWindow = 50
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.7)
	params.PocParams.PocV2Enabled = true
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.PrecomputeSPRTValues(ctx))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 0}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 3, PocStartBlockHeight: 2000}))
	return NewAppModule(nil, k, nil, nil, nil, nil), k, ctx
}

func seedChallengeParticipant(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
	t.Helper()
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "m1", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			Weight:             100,
			ConfirmationWeight: 100,
		}},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 2,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: []string{"m1"},
			MlNodes: []*types.ModelMLNodes{{
				MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}},
			}},
		}},
	}))
}

func TestDecideCurrentChallengeSegment_AbortedOnMissingSnapshot(t *testing.T) {
	am, k, ctx := challengeApp(t)
	seedChallengeParticipant(t, k, ctx)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED, ch.State)
	require.Equal(t, int64(100), ch.StartHeight)
}

func TestDecideCurrentChallengeSegment_ZeroCommitFails(t *testing.T) {
	am, k, ctx := challengeApp(t)
	seedChallengeParticipant(t, k, ctx)
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 180,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: "m1",
			VotingPowers: []*types.VotingPowerEntry{{
				Address:     testutil.Validator,
				VotingPower: 100,
			}},
		}},
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	p, ok := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	p.Status = types.ParticipantStatus_INACTIVE
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), p))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED, ch.State)
	p, ok = k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	require.NotNil(t, p.CurrentEpochStats.ConfirmationPoCRatio)
}

func TestDecideCurrentChallengeSegment_ShortSegmentRotates(t *testing.T) {
	am, k, ctx := challengeApp(t)
	seedChallengeParticipant(t, k, ctx)
	p, ok := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	p.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromFloat(0.8)
	require.NoError(t, k.SetParticipant(ctx, p))
	ctx = ctx.WithBlockHeight(400)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 350,
		Seed:        []byte{1},
	}))
	require.NoError(t, k.PoCChallengeCommits.Set(ctx, collections.Join(sdk.MustAccAddressFromBech32(testutil.Executor), "m1"), types.PoCV2StoreCommit{
		ParticipantAddress: testutil.Executor,
		ModelId:            "m1",
		Count:              3,
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 400, 180, true))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
	require.Equal(t, int64(400), ch.StartHeight)
	require.Equal(t, ctx.HeaderInfo().Hash, ch.Seed)
	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Empty(t, commits)
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(100), group.ValidationWeights[0].ConfirmationWeight)
	p, ok = k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	require.NotNil(t, p.CurrentEpochStats.ConfirmationPoCRatio)
	require.True(t, p.CurrentEpochStats.ConfirmationPoCRatio.ToDecimal().Equal(types.DecimalFromFloat(0.8).ToDecimal()))
}

func TestDecideCurrentChallengeSegment_ReplayAfterRotateDoesNotAbort(t *testing.T) {
	am, k, ctx := challengeApp(t)
	ctx = ctx.WithBlockHeight(400)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 350,
		Seed:        []byte{1},
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 400, 180, true))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
	require.Equal(t, int64(400), ch.StartHeight)

	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 400, 180, true))
	ch, found, err = k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
	require.Equal(t, int64(400), ch.StartHeight)
}

func TestDecideCurrentChallengeSegment_NoRotateClosesBeforeLastSegment(t *testing.T) {
	am, k, ctx := challengeApp(t)
	ctx = ctx.WithBlockHeight(1960)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
		Seed:        []byte{1},
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 200, 180, true))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
	require.Equal(t, int64(1950), ch.StartHeight)

	require.NoError(t, am.decideLastChallengeSegments(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 0}))
	ch, found, err = k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
	require.Equal(t, int64(1950), ch.StartHeight)
}

func TestDecideCurrentChallengeSegment_MissingTargetDoesNotWriteWeight(t *testing.T) {
	am, k, ctx := challengeApp(t)
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 2,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "m1", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			Weight:             100,
			ConfirmationWeight: 100,
		}},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 2,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: []string{"m1"},
			MlNodes: []*types.ModelMLNodes{{
				MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}},
			}},
		}},
	}))
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 180,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: "m1",
			VotingPowers: []*types.VotingPowerEntry{{
				Address:     testutil.Validator,
				VotingPower: 100,
			}},
		}},
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED, ch.State)
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(100), group.ValidationWeights[0].ConfirmationWeight)
}

func TestDecideCurrentChallengeSegment_FinalShortSegmentPasses(t *testing.T) {
	am, k, ctx := challengeApp(t)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 1900,
		Seed:        []byte{1},
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 1950, 2000, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
	require.Equal(t, int64(1900), ch.StartHeight)
}

func TestStripChallengeSkipFromPreserved(t *testing.T) {
	snapshot := types.PreservedNodesSnapshot{
		ModelPreservedNodes: []*types.ModelPreservedNodes{{
			ModelId: "m1",
			Participants: []*types.ParticipantPreservedNodes{
				{ParticipantId: testutil.Executor},
				{ParticipantId: testutil.Validator},
			},
		}},
	}
	got := stripChallengeSkipFromPreserved(snapshot, map[string]struct{}{testutil.Executor: {}})
	require.Len(t, got.ModelPreservedNodes[0].Participants, 1)
	require.Equal(t, testutil.Validator, got.ModelPreservedNodes[0].Participants[0].ParticipantId)
}

func TestSameEpochTargetsSkipInferenceMiss(t *testing.T) {
	_, k, ctx := challengeApp(t)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.True(t, k.IsUnderChallenge(ctx, testutil.Executor))
	ctx = ctx.WithBlockHeight(1960)
	require.False(t, k.IsUnderChallenge(ctx, testutil.Executor))
}

type challengeEvalModel struct {
	id     string
	weight int64
	count  uint32
	accept bool
}

func prepareChallengeEval(t *testing.T, stage, exchange int64, models []challengeEvalModel) (AppModule, keeper.Keeper, sdk.Context) {
	t.Helper()
	am, k, ctx := challengeApp(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.PocStageDuration = stage
	params.EpochParams.PocExchangeDuration = exchange
	params.PocParams.ValidationSlots = 0
	require.NoError(t, k.SetParams(ctx, params))

	modelIDs := make([]string, 0, len(models))
	scales := make([]*types.ConfirmationWeightScale, 0, len(models))
	mlNodes := make([]*types.ModelMLNodes, 0, len(models))
	modelVPs := make([]*types.ModelVotingPowers, 0, len(models))
	expected := int64(0)
	for _, m := range models {
		modelIDs = append(modelIDs, m.id)
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:              m.id,
			EffectiveCoefficient: types.DecimalFromFloat(1),
		})
		mlNodes = append(mlNodes, &types.ModelMLNodes{
			MlNodes: []*types.MLNodeInfo{{NodeId: m.id + "-n", PocWeight: m.weight}},
		})
		modelVPs = append(modelVPs, &types.ModelVotingPowers{
			ModelId: m.id,
			VotingPowers: []*types.VotingPowerEntry{{
				Address:     testutil.Validator,
				VotingPower: 100,
			}},
		})
		expected += m.weight
	}

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      "vk",
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  2,
		Signature:   "seed",
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:               2,
		ConfirmationWeightScales: scales,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      testutil.Executor,
			Weight:             expected,
			ConfirmationWeight: expected,
		}},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 2,
		Participants: []*types.ActiveParticipant{{
			Index:   testutil.Executor,
			Models:  modelIDs,
			MlNodes: mlNodes,
		}},
	}))
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 180,
		TotalNetworkWeight:  100,
		ModelVotingPowers:   modelVPs,
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))

	target := sdk.MustAccAddressFromBech32(testutil.Executor)
	validator := sdk.MustAccAddressFromBech32(testutil.Validator)
	for _, m := range models {
		if m.count == 0 {
			continue
		}
		require.NoError(t, k.PoCChallengeCommits.Set(ctx, collections.Join(target, m.id), types.PoCV2StoreCommit{
			ParticipantAddress: testutil.Executor,
			ModelId:            m.id,
			Count:              m.count,
		}))
		voteWeight := int64(m.count)
		if !m.accept {
			voteWeight = 0
		}
		require.NoError(t, k.PoCChallengeValidations.Set(ctx, collections.Join3(target, m.id, validator), types.PoCValidationV2{
			ParticipantAddress:          testutil.Executor,
			ValidatorParticipantAddress: testutil.Validator,
			ValidatedWeight:             voteWeight,
			ModelId:                     m.id,
		}))
	}
	return am, k, ctx
}

func TestDecideCurrentChallengeSegment_NormalizationScalesShortSegment(t *testing.T) {
	am, k, ctx := prepareChallengeEval(t, 800, 0, []challengeEvalModel{
		{id: "m1", weight: 100, count: 50, accept: true},
	})
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(100), group.ValidationWeights[0].ConfirmationWeight)
}

func TestDecideCurrentChallengeSegment_PartialRatioHaircut(t *testing.T) {
	am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
		{id: "m1", weight: 100, count: 80, accept: true},
	})
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(80), group.ValidationWeights[0].ConfirmationWeight)
	p, ok := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	require.NotNil(t, p.CurrentEpochStats.ConfirmationPoCRatio)
	got := p.CurrentEpochStats.ConfirmationPoCRatio.ToDecimal()
	require.True(t, got.GreaterThan(decimal.NewFromFloat(0.7)))
	require.True(t, got.LessThan(decimal.NewFromInt(1)))
}

func TestDecideCurrentChallengeSegment_PartialRatioFail(t *testing.T) {
	am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
		{id: "m1", weight: 100, count: 50, accept: true},
	})
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED, ch.State)
}

func TestDecideCurrentChallengeSegment_MultiModelPartialAcceptance(t *testing.T) {
	am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
		{id: "m1", weight: 80, count: 80, accept: true},
		{id: "m2", weight: 20, count: 20, accept: false},
	})
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(80), group.ValidationWeights[0].ConfirmationWeight)
}

func TestDecideCurrentChallengeSegments_AbortDoesNotRollBackSibling(t *testing.T) {
	am, k, ctx := challengeApp(t)
	seedChallengeParticipant(t, k, ctx)
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor2,
		Address:           testutil.Executor2,
		Status:            types.ParticipantStatus_INACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	group.ValidationWeights = append(group.ValidationWeights, &types.ValidationWeight{
		MemberAddress:      testutil.Executor2,
		Weight:             100,
		ConfirmationWeight: 100,
	})
	k.SetEpochGroupData(ctx, group)
	participants, found := k.GetActiveParticipants(ctx, 2)
	require.True(t, found)
	participants.Participants = append(participants.Participants, &types.ActiveParticipant{
		Index:  testutil.Executor2,
		Models: []string{"m1"},
		MlNodes: []*types.ModelMLNodes{{
			MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}},
		}},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, participants))
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 180,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: "m1",
			VotingPowers: []*types.VotingPowerEntry{{
				Address:     testutil.Validator,
				VotingPower: 100,
			}},
		}},
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Creator,
		StartHeight: 100,
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor2,
		StartHeight: 100,
	}))

	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))

	aborted, found, err := k.GetPoCChallenge(ctx, testutil.Creator)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED, aborted.State)
	failed, found, err := k.GetPoCChallenge(ctx, testutil.Executor2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED, failed.State)
}

func TestDecideCurrentChallengeSegments_SkipsOtherEpoch(t *testing.T) {
	am, k, ctx := challengeApp(t)
	seedChallengeParticipant(t, k, ctx)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  1,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
	require.Equal(t, uint64(1), ch.EpochIndex)
}

func TestDecideLastChallengeSegments_PropagatesWhenUpcomingMissing(t *testing.T) {
	am, k, ctx := challengeApp(t)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.NoError(t, k.Epochs.Remove(ctx, 3))
	err := am.decideLastChallengeSegments(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 0})
	require.Error(t, err)
	ch, found, getErr := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, getErr)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
}
