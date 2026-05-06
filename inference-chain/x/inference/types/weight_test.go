package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestConfirmationWeightOfModelNodes(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: &types.Decimal{Value: 2, Exponent: 0}},
		{ModelId: "model-b"},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-a": {
			&types.MLNodeInfo{PocWeight: 10},
			nil,
			&types.MLNodeInfo{PocWeight: 5},
		},
		"model-b": {
			&types.MLNodeInfo{PocWeight: 7},
		},
		"model-c": {
			&types.MLNodeInfo{PocWeight: 100},
		},
	}

	require.Equal(t, int64(37), types.ConfirmationWeightOfModelNodes(modelNodes, scales))
}

func TestConfirmationWeightOfParticipantMatchesModelNodes(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: &types.Decimal{Value: 15, Exponent: -1}},
		{ModelId: "model-b", WeightScaleFactor: &types.Decimal{Value: 2, Exponent: 0}},
	}
	participant := &types.ActiveParticipant{
		Models: []string{"model-a", "model-b", "model-c"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{&types.MLNodeInfo{PocWeight: 10}}},
			{MlNodes: []*types.MLNodeInfo{&types.MLNodeInfo{PocWeight: 3}, &types.MLNodeInfo{PocWeight: 4}}},
			{MlNodes: []*types.MLNodeInfo{&types.MLNodeInfo{PocWeight: 100}}},
		},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-a": participant.MlNodes[0].MlNodes,
		"model-b": participant.MlNodes[1].MlNodes,
		"model-c": participant.MlNodes[2].MlNodes,
	}

	require.Equal(t,
		types.ConfirmationWeightOfModelNodes(modelNodes, scales),
		types.ConfirmationWeightOfParticipant(participant, scales),
	)
	require.Equal(t, int64(29), types.ConfirmationWeightOfParticipant(participant, scales))
}

func TestConfirmationWeightWithCoefficientsMatchesConvenienceFunctions(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: &types.Decimal{Value: 2, Exponent: 0}},
		{ModelId: "model-b", WeightScaleFactor: &types.Decimal{Value: 3, Exponent: 0}},
	}
	participant := &types.ActiveParticipant{
		Models: []string{"model-b", "model-a"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 4}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 5}}},
		},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-a": participant.MlNodes[1].MlNodes,
		"model-b": participant.MlNodes[0].MlNodes,
	}

	coefficients := types.ConfirmationWeightCoefficients(scales)
	require.Equal(t,
		types.ConfirmationWeightOfModelNodes(modelNodes, scales),
		types.ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, coefficients),
	)
	require.Equal(t,
		types.ConfirmationWeightOfParticipant(participant, scales),
		types.ConfirmationWeightOfParticipantWithCoefficients(participant, coefficients),
	)
}

func TestConfirmationWeightEmptyInputs(t *testing.T) {
	require.Zero(t, types.ConfirmationWeightOfParticipant(nil, nil))
	require.Zero(t, types.ConfirmationWeightOfModelNodes(nil, nil))
	require.Zero(t, types.ConfirmationWeightOfModelNodes(map[string][]*types.MLNodeInfo{
		"model-a": {&types.MLNodeInfo{PocWeight: 1}},
	}, nil))
}
