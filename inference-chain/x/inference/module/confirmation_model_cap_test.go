package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCappedReadingByParticipant(t *testing.T) {
	expected := participantModelWeights{
		"host": {"model-a": 40, "model-b": 60},
		"idle": {"model-a": 10},
	}

	reading := cappedReadingByParticipant(
		expected,
		participantModelWeights{"host": {"model-a": 100}},
		participantModelWeights{"host": {"model-b": 0}},
	)
	require.Equal(t, int64(40), reading["host"])
	require.Equal(t, int64(0), reading["idle"])

	reading = cappedReadingByParticipant(
		expected,
		participantModelWeights{"host": {"model-a": 40}},
		participantModelWeights{"host": {"model-b": 60}},
	)
	require.Equal(t, int64(100), reading["host"])

	reading = cappedReadingByParticipant(
		expected,
		nil,
		participantModelWeights{"host": {"model-a": 100, "model-b": 60, "foreign": 500}},
	)
	require.Equal(t, int64(100), reading["host"])

	reading = cappedReadingByParticipant(
		expected,
		nil,
		participantModelWeights{"host": {"model-a": 80, "model-b": 30}},
	)
	require.Equal(t, int64(70), reading["host"])

	reading = cappedReadingByParticipant(
		expected,
		participantModelWeights{"host": {"model-a": -8}},
		participantModelWeights{"host": {"model-a": 3, "model-b": 90}},
	)
	require.Equal(t, int64(60), reading["host"])

	require.Empty(t, cappedReadingByParticipant(nil, expected, expected))
}

func TestWeightByParticipantAndModelPreservesTruncationAndExclusions(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", EffectiveCoefficient: &types.Decimal{Value: 1, Exponent: -1}},
		{ModelId: "model-b", EffectiveCoefficient: &types.Decimal{Value: 2, Exponent: 0}},
		{
			ModelId:                 "excluded",
			EffectiveCoefficient:    &types.Decimal{Value: 9, Exponent: 0},
			ExcludeFromConfirmation: true,
		},
		{ModelId: "absent", EffectiveCoefficient: types.DecimalFromFloat(4)},
	}
	host := &types.ActiveParticipant{
		Index:  "host",
		Models: []string{"model-b", "model-a", "model-a", "excluded", "absent", ""},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 3}, {PocWeight: 4}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 5}, nil}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 5}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 100}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 10}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 100}}},
		},
	}
	reversed := &types.ActiveParticipant{
		Index:  host.Index,
		Models: []string{"", "absent", "excluded", "model-a", "model-a", "model-b"},
		MlNodes: []*types.ModelMLNodes{
			host.MlNodes[5],
			host.MlNodes[4],
			host.MlNodes[3],
			host.MlNodes[2],
			host.MlNodes[1],
			host.MlNodes[0],
		},
	}
	noEligible := &types.ActiveParticipant{
		Index:  "empty",
		Models: []string{"excluded"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{nil}},
		},
	}
	laterDuplicate := &types.ActiveParticipant{
		Index:  "host",
		Models: []string{"model-b"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 1}}},
		},
	}

	present := confirmationScalesInSnapshot(scales, []*types.ModelVotingPowers{
		{ModelId: "model-a"},
		{ModelId: "model-b"},
		{ModelId: "excluded"},
	})
	require.Equal(t, []string{"model-a", "model-b", "excluded"}, confirmationModelIDs(present))

	byModel := weightByParticipantAndModel([]*types.ActiveParticipant{nil, host, noEligible}, present)
	require.Equal(t, map[string]int64{"model-a": 1, "model-b": 14}, byModel["host"])
	require.Equal(t, byModel["host"], weightByParticipantAndModel([]*types.ActiveParticipant{reversed}, present)["host"])
	require.Equal(t, int64(15), weightByParticipant([]*types.ActiveParticipant{host}, present)["host"])
	require.Equal(t, types.ConfirmationWeightOfParticipant(host, present), int64(15))
	require.Zero(t, weightByParticipant([]*types.ActiveParticipant{noEligible}, present)["empty"])

	duplicate := weightByParticipantAndModel([]*types.ActiveParticipant{host, laterDuplicate}, present)
	require.Equal(t, map[string]int64{"model-b": 2}, duplicate["host"])

	measured := weightByParticipantAndModel([]*types.ActiveParticipant{{
		Index:  "host",
		Models: []string{"model-a", "model-b", "absent"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 1000}}},
			nil,
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 1000}}},
		},
	}}, present)
	reading := cappedReadingByParticipant(byModel, nil, measured)
	require.Equal(t, int64(1), reading["host"])
	require.NotContains(t, measured["host"], "absent")
	require.NotContains(t, measured["host"], "excluded")
}

func confirmationModelIDs(scales []*types.ConfirmationWeightScale) []string {
	ids := make([]string, 0, len(scales))
	for _, scale := range scales {
		ids = append(ids, scale.ModelId)
	}
	return ids
}

func TestEvaluateConfirmation_OwnModelReadingIsCapped(t *testing.T) {
	const expectedTotal = int64(100)

	tests := []struct {
		name      string
		measuredA int64
		measuredB int64
		foreignC  int64
		preserveA bool
		want      int64
	}{
		{name: "surplus on A does not cover missing B", measuredA: 100, want: 40},
		{name: "balanced work", measuredA: 40, measuredB: 60, want: expectedTotal},
		{name: "surplus above a complete pair stays at expected", measuredA: 100, measuredB: 60, want: expectedTotal},
		{name: "partial work on each model", measuredA: 80, measuredB: 30, want: 70},
		{name: "preserved A plus measured B", measuredB: 60, preserveA: true, want: expectedTotal},
		{name: "preserved and measured surplus on A does not cover missing B", measuredA: 100, preserveA: true, want: 40},
		{name: "foreign model stays excluded", measuredA: 40, foreignC: 100, want: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am, k, ctx := seedOwnModelConfirmation(t, 180)
			if tt.preserveA {
				require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
					EpisodeAnchorHeight: 180,
					ModelPreservedNodes: []*types.ModelPreservedNodes{{
						ModelId: "model-a",
						Participants: []*types.ParticipantPreservedNodes{{
							ParticipantId: testutil.Executor,
							NodeIds:       []string{"node-a"},
						}},
					}},
				}))
			}
			commitConfirmationModel(t, k, ctx, "model-a", tt.measuredA, 180)
			commitConfirmationModel(t, k, ctx, "model-b", tt.measuredB, 180)
			commitConfirmationModel(t, k, ctx, "model-c", tt.foreignC, 180)

			if tt.foreignC > 0 {
				snapshot, found, err := k.GetPoCValidationSnapshot(ctx, 180)
				require.NoError(t, err)
				require.True(t, found)
				calculated := am.updateConfirmationWeightsV2(ctx, &types.ConfirmationPoCEvent{
					EpochIndex:    2,
					TriggerHeight: 180,
				}, snapshot)
				require.Len(t, calculated, 1)
				require.Contains(t, calculated[0].Models, "model-c")
			}

			require.NoError(t, am.updateConfirmationWeights(ctx, confirmationEvent(180)))

			group, found := k.GetEpochGroupData(ctx, 2, "")
			require.True(t, found)
			require.Equal(t, tt.want, group.ValidationWeights[0].ConfirmationWeight)

			participant, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			requireConfirmationRatio(t, participant, tt.want, expectedTotal)
			if confirmationRatio(tt.want, expectedTotal).LessThan(decimal.NewFromFloat(0.5)) {
				require.Equal(t, types.ParticipantStatus_INACTIVE, participant.Status)
			} else {
				require.Equal(t, types.ParticipantStatus_ACTIVE, participant.Status)
			}
		})
	}
}

func TestEvaluateConfirmation_LaterEventDoesNotRaiseConfirmationWeight(t *testing.T) {
	am, k, ctx := seedOwnModelConfirmation(t, 180)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0)
	require.NoError(t, k.SetParams(ctx, params))

	commitConfirmationModel(t, k, ctx, "model-a", 100, 180)
	require.NoError(t, am.updateConfirmationWeights(ctx, confirmationEvent(180)))
	group, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(40), group.ValidationWeights[0].ConfirmationWeight)

	am.captureConfirmationValidationSnapshot(ctx, 280, 280)
	commitConfirmationModel(t, k, ctx, "model-a", 40, 280)
	commitConfirmationModel(t, k, ctx, "model-b", 60, 280)
	require.NoError(t, am.updateConfirmationWeights(ctx, confirmationEvent(280)))

	group, found = k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, int64(40), group.ValidationWeights[0].ConfirmationWeight)
	participant, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, types.ParticipantStatus_ACTIVE, participant.Status)
}

func seedOwnModelConfirmation(t *testing.T, triggerHeight int64) (AppModule, keeper.Keeper, sdk.Context) {
	t.Helper()
	const epoch = uint64(2)

	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "model-c", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.5)
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.PrecomputeSPRTValues(ctx))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))

	host := &types.ActiveParticipant{
		Index:  testutil.Executor,
		Weight: 100,
		Models: []string{"model-a", "model-b"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{NodeId: "node-a", PocWeight: 40}}},
			{MlNodes: []*types.MLNodeInfo{{NodeId: "node-b", PocWeight: 60}}},
		},
	}
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", EffectiveCoefficient: types.DecimalFromFloat(1)},
		{ModelId: "model-b", EffectiveCoefficient: types.DecimalFromFloat(1)},
		{ModelId: "model-c", EffectiveCoefficient: types.DecimalFromFloat(1)},
	}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:               epoch,
		EpochGroupId:             77,
		SubGroupModels:           []string{"model-a", "model-b", "model-c"},
		ConfirmationWeightScales: scales,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: host.Index, Weight: host.Weight, ConfirmationWeight: 100},
			{MemberAddress: testutil.Validator, Weight: 400, ConfirmationWeight: 400},
		},
	})
	for i, modelID := range []string{"model-a", "model-b", "model-c"} {
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex:   epoch,
			EpochGroupId: uint64(78 + i),
			ModelId:      modelID,
			ValidationWeights: []*types.ValidationWeight{
				{MemberAddress: testutil.Validator, Weight: 400, VotingPower: 400},
			},
		})
	}
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		Participants: []*types.ActiveParticipant{host},
	}))
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		ValidatorKey:      "validator-key",
		InferenceUrl:      "http://executor.example.com",
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}))
	k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  epoch,
		Signature:   "seed-signature",
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	am.captureConfirmationValidationSnapshot(ctx, triggerHeight, triggerHeight)
	_, found, err := k.GetPoCValidationSnapshot(ctx, triggerHeight)
	require.NoError(t, err)
	require.True(t, found)
	return am, k, ctx
}

func commitConfirmationModel(t *testing.T, k keeper.Keeper, ctx sdk.Context, modelID string, weight, height int64) {
	t.Helper()
	if weight <= 0 {
		return
	}
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: height,
		Count:                    uint32(weight),
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        height,
		ModelId:                  modelID,
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: height,
		ModelId:                  modelID,
		Weights:                  []*types.MLNodeWeight{{NodeId: "node-" + modelID, Weight: uint32(weight)}},
	}))
	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    height,
		ValidatedWeight:             weight,
		ModelId:                     modelID,
	}))
}

func confirmationEvent(height int64) *types.ConfirmationPoCEvent {
	return &types.ConfirmationPoCEvent{
		EpochIndex:    2,
		TriggerHeight: height,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}
}

func confirmationRatio(reading, expected int64) decimal.Decimal {
	ratio := decimal.NewFromInt(reading).Div(decimal.NewFromInt(expected)).Div(pocDeviationCoeff)
	if ratio.GreaterThan(decimal.NewFromInt(1)) {
		return decimal.NewFromInt(1)
	}
	return ratio
}

func requireConfirmationRatio(t *testing.T, participant types.Participant, reading, expected int64) {
	t.Helper()
	require.NotNil(t, participant.CurrentEpochStats)
	require.NotNil(t, participant.CurrentEpochStats.ConfirmationPoCRatio)
	got := participant.CurrentEpochStats.ConfirmationPoCRatio.ToDecimal()
	want := confirmationRatio(reading, expected)
	require.True(t, got.Equal(want), "got ratio %s, expected %s", got.String(), want.String())
}
