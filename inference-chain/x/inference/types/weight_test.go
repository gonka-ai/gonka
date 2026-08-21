package types_test

import (
	"math"
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

func TestEffectiveConfirmedWeight(t *testing.T) {
	tests := []struct {
		name               string
		weight             int64
		confirmationWeight int64
		rawTotal           int64
		expected           int64
	}{
		{name: "fully confirmed", weight: 100, confirmationWeight: 50, rawTotal: 50, expected: 100},
		{name: "partially confirmed", weight: 100, confirmationWeight: 30, rawTotal: 50, expected: 60},
		{name: "nothing confirmed", weight: 100, confirmationWeight: 0, rawTotal: 50, expected: 0},
		{name: "truncates toward zero", weight: 10, confirmationWeight: 1, rawTotal: 3, expected: 3},
		{name: "clamped to weight when over-confirmed", weight: 100, confirmationWeight: 80, rawTotal: 50, expected: 100},
		{name: "zero weight", weight: 0, confirmationWeight: 50, rawTotal: 50, expected: 0},
		{name: "negative weight", weight: -5, confirmationWeight: 50, rawTotal: 50, expected: 0},
		{name: "zero raw total", weight: 100, confirmationWeight: 50, rawTotal: 0, expected: 0},
		{name: "negative raw total", weight: 100, confirmationWeight: 50, rawTotal: -1, expected: 0},
		{name: "negative confirmation treated as zero", weight: 100, confirmationWeight: -10, rawTotal: 50, expected: 0},
		{name: "over-confirmed MaxInt64 does not wrap", weight: math.MaxInt64, confirmationWeight: math.MaxInt64, rawTotal: 1, expected: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, types.EffectiveConfirmedWeight(tt.weight, tt.confirmationWeight, tt.rawTotal))
		})
	}
}

func TestConfirmationWeightEmptyInputs(t *testing.T) {
	require.Zero(t, types.ConfirmationWeightOfParticipant(nil, nil))
	require.Zero(t, types.ConfirmationWeightOfModelNodes(nil, nil))
	require.Zero(t, types.ConfirmationWeightOfModelNodes(map[string][]*types.MLNodeInfo{
		"model-a": {&types.MLNodeInfo{PocWeight: 1}},
	}, nil))
}

func TestEffectiveWeightFromModels_SeparatedPolicies(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: true},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: false},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-a": {{PocWeight: 10}},
		"model-b": {{PocWeight: 90}},
	}

	// Trusted model fully confirmed (10); untrusted 90 still in the full denominator.
	require.Equal(t, int64(100), types.EffectiveWeightFromModels(
		100, scales, true, modelNodes, 10, types.RewardWeightPolicy,
	), "untrusted raw weight still earns rewards")
	require.Equal(t, int64(10), types.EffectiveWeightFromModels(
		100, scales, true, modelNodes, 10, types.TrustWeightPolicy,
	), "untrusted weight does not become next-epoch cap")
}

func TestEffectiveWeightFromModels_UntrustedOnly(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: false},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-b": {{PocWeight: 100}},
	}

	require.Equal(t, int64(100), types.EffectiveWeightFromModels(
		100, scales, true, modelNodes, 0, types.RewardWeightPolicy,
	))
	require.Equal(t, int64(0), types.EffectiveWeightFromModels(
		100, scales, true, modelNodes, 0, types.TrustWeightPolicy,
	))
}

func TestEffectiveWeightFromModels_LegacyUnseparatedMatchesOldFormula(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1)},
	}
	modelNodes := map[string][]*types.MLNodeInfo{
		"model-a": {{PocWeight: 10}},
		"model-b": {{PocWeight: 90}},
	}
	rawAll := types.ConfirmationWeightOfModelNodes(modelNodes, scales)
	require.Equal(t,
		types.EffectiveConfirmedWeight(100, 10, rawAll),
		types.EffectiveWeightFromModels(100, scales, false, modelNodes, 10, types.TrustWeightPolicy),
	)
	require.Equal(t,
		types.EffectiveConfirmedWeight(100, 10, rawAll),
		types.EffectiveWeightFromModels(100, scales, false, modelNodes, 10, types.RewardWeightPolicy),
	)
}
