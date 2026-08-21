package inference

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestBuildConfirmationWeightScales_IncludesUntrustedRealModels(t *testing.T) {
	params := types.DefaultParams()
	params.PocParams.Models = []*types.PoCModelConfig{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(2)},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  "trusted-host",
			Models: []string{"model-a"},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{PocWeight: 10}}},
			},
			VotingPowers: []*types.ModelVotingPower{
				{ModelId: "model-a", VotingPower: 50},
			},
		},
		{
			Index:  "new-host",
			Models: []string{"model-b"},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{PocWeight: 90}}},
			},
		},
	}

	scales := buildConfirmationWeightScales([]string{"model-a", "model-b"}, participants, params.PocParams)
	require.Equal(t, []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: true},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(2), HasTrustedVotingPower: false},
	}, scales)
}

func TestBuildConfirmationWeightScales_OmitsIneligibleModels(t *testing.T) {
	params := types.DefaultParams()
	params.PocParams.Models = []*types.PoCModelConfig{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		{ModelId: "gone", WeightScaleFactor: types.DecimalFromFloat(1)},
	}
	participants := []*types.ActiveParticipant{
		{
			Index:  "host",
			Models: []string{"model-a", "gone"},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{PocWeight: 10}}},
				{MlNodes: []*types.MLNodeInfo{{PocWeight: 90}}},
			},
			VotingPowers: []*types.ModelVotingPower{
				{ModelId: "model-a", VotingPower: 10},
			},
		},
	}

	scales := buildConfirmationWeightScales([]string{"model-a"}, participants, params.PocParams)
	require.Len(t, scales, 1)
	require.Equal(t, "model-a", scales[0].ModelId)
	require.True(t, scales[0].HasTrustedVotingPower)
}
