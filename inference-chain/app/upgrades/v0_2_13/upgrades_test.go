package v0_2_13

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.13", UpgradeName)
}

func TestBackfillConfirmationWeightScales(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "model-a", WeightScaleFactor: inferencetypes.DecimalFromFloat(2)},
		{ModelId: "model-b", WeightScaleFactor: inferencetypes.DecimalFromFloat(3)},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))

	alice := sample.AccAddress()
	bob := sample.AccAddress()

	require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId: 7,
		Participants: []*inferencetypes.ActiveParticipant{
			{
				Index:  alice,
				Models: []string{"model-a", "model-b", "model-c"},
				MlNodes: []*inferencetypes.ModelMLNodes{
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 10}}},
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 20}}},
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 30}}},
				},
			},
			{
				Index:  bob,
				Models: []string{"model-a"},
				MlNodes: []*inferencetypes.ModelMLNodes{
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 5}}},
				},
			},
		},
	}))

	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, Weight: 100, ConfirmationWeight: 999},
			{MemberAddress: bob, Weight: 50, ConfirmationWeight: 5},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-a",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 100},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-b",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 0},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-c",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 1},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 8,
		ModelId:    "model-d",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 1},
		},
	})

	require.NoError(t, backfillConfirmationWeightScales(ctx, k))

	root, found := k.GetEpochGroupData(ctx, 7, "")
	require.True(t, found)
	require.Len(t, root.ConfirmationWeightScales, 2)
	require.Equal(t, "model-a", root.ConfirmationWeightScales[0].ModelId)
	require.True(t, root.ConfirmationWeightScales[0].WeightScaleFactor.ToDecimal().Equal(inferencetypes.DecimalFromFloat(2).ToDecimal()))
	require.Equal(t, "model-c", root.ConfirmationWeightScales[1].ModelId)
	require.True(t, root.ConfirmationWeightScales[1].WeightScaleFactor.ToDecimal().Equal(inferencetypes.DecimalFromFloat(1).ToDecimal()))

	require.Equal(t, int64(50), root.ValidationWeights[0].ConfirmationWeight)
	require.Equal(t, int64(5), root.ValidationWeights[1].ConfirmationWeight)
}

func TestUpdateModelParamsSetsKimiAndAddsMiniMax(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{
			ModelId:           kimiModelID,
			WeightScaleFactor: inferencetypes.DecimalFromFloat(1),
		},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 11))
	k.SetModel(ctx, &inferencetypes.Model{
		Id:                  qwenModelID,
		HfRepo:              qwenModelID,
		ValidationThreshold: &inferencetypes.Decimal{Value: 958, Exponent: -3},
	})
	k.SetModel(ctx, &inferencetypes.Model{
		Id:                  kimiModelID,
		HfRepo:              kimiModelID,
		ValidationThreshold: &inferencetypes.Decimal{Value: 920, Exponent: -3},
		ModelArgs: []string{
			"--max-model-len", "240000",
			"--tool-call-parser", "kimi_k2",
			"--reasoning-parser", "kimi_k2",
		},
	})

	require.NoError(t, updateModelParams(ctx, k))

	qwenModel, found := k.GetGovernanceModel(ctx, qwenModelID)
	require.True(t, found)
	require.Equal(t, &inferencetypes.Decimal{Value: 940, Exponent: -3}, qwenModel.ValidationThreshold)

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Len(t, got.PocParams.Models, 2)

	kimi := requirePoCModelConfig(t, got.PocParams.Models, kimiModelID)
	require.Equal(t, &inferencetypes.Decimal{Value: 78, Exponent: -2}, kimi.WeightScaleFactor)
	kimiModel, found := k.GetGovernanceModel(ctx, kimiModelID)
	require.True(t, found)
	require.Equal(t, []string{
		"--enable-auto-tool-choice",
		"--max-model-len", "240000",
		"--tool-call-parser", "kimi_k2",
		"--reasoning-parser", "kimi_k2",
	}, kimiModel.ModelArgs)
	require.Equal(t, &inferencetypes.Decimal{Value: 900, Exponent: -3}, kimiModel.ValidationThreshold)

	minimax := requirePoCModelConfig(t, got.PocParams.Models, minimaxModelID)
	require.Equal(t, int64(1024), minimax.SeqLen)
	require.NotNil(t, minimax.StatTest)
	require.Equal(t, &inferencetypes.Decimal{Value: 75, Exponent: -2}, minimax.StatTest.DistThreshold)
	require.Equal(t, &inferencetypes.Decimal{Value: 1, Exponent: -1}, minimax.StatTest.PMismatch)
	require.Equal(t, &inferencetypes.Decimal{Value: 5, Exponent: -2}, minimax.StatTest.PValueThreshold)
	require.Equal(t, &inferencetypes.Decimal{Value: 3024, Exponent: -4}, minimax.WeightScaleFactor)
	require.Equal(t, uint64(271), minimax.PenaltyStartEpoch)

	model, found := k.GetGovernanceModel(ctx, minimaxModelID)
	require.True(t, found)
	require.Equal(t, minimaxModelID, model.Id)
	require.Equal(t, "d494266a4affc0d2995ba1fa35c8481cbd84294b", model.HfCommit)
	require.Equal(t, uint64(320), model.VRam)
	require.Equal(t, uint64(5000), model.ThroughputPerNonce)
	require.Equal(t, &inferencetypes.Decimal{Value: 922, Exponent: -3}, model.ValidationThreshold)
	require.Equal(t, []string{
		"--enable-auto-tool-choice",
		"--kv-cache-dtype", "fp8",
		"--tool-call-parser", "minimax_m2",
		"--reasoning-parser", "minimax_m2_append_think",
	}, model.ModelArgs)

	require.NoError(t, updateModelParams(ctx, k))
	got, err = k.GetParams(ctx)
	require.NoError(t, err)
	require.Len(t, got.PocParams.Models, 2)
	kimiModel, found = k.GetGovernanceModel(ctx, kimiModelID)
	require.True(t, found)
	require.Equal(t, []string{
		"--enable-auto-tool-choice",
		"--max-model-len", "240000",
		"--tool-call-parser", "kimi_k2",
		"--reasoning-parser", "kimi_k2",
	}, kimiModel.ModelArgs)
}

func requirePoCModelConfig(
	t *testing.T,
	models []*inferencetypes.PoCModelConfig,
	modelID string,
) *inferencetypes.PoCModelConfig {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ModelId == modelID {
			return model
		}
	}
	require.Failf(t, "missing PoC model config", "model_id=%s", modelID)
	return nil
}
