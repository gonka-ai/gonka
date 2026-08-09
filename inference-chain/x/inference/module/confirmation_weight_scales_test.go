package inference

import (
	"testing"

	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBuildConfirmationWeightScalesUsesEpochEffectiveCoefficient(t *testing.T) {
	participants := []*types.ActiveParticipant{{
		VotingPowers: []*types.ModelVotingPower{{
			ModelId:     "model-a",
			VotingPower: 10,
		}},
	}}
	result := &coefficient.Result{
		Scales: []*types.ConfirmationWeightScale{
			{ModelId: "model-a", EffectiveCoefficient: &types.Decimal{Value: 125, Exponent: -2}},
			{ModelId: "model-b", EffectiveCoefficient: &types.Decimal{Value: 2, Exponent: 0}},
		},
	}

	scales := buildConfirmationWeightScales(
		[]string{"model-a", "model-b"},
		participants,
		result,
	)
	require.Equal(t, []*types.ConfirmationWeightScale{{
		ModelId:              "model-a",
		EffectiveCoefficient: &types.Decimal{Value: 125, Exponent: -2},
	}, {
		ModelId:                 "model-b",
		EffectiveCoefficient:    &types.Decimal{Value: 2, Exponent: 0},
		ExcludeFromConfirmation: true,
	}}, scales)
}
