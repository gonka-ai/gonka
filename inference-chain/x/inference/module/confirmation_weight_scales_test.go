package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
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
	result := &epochCoefficientResult{
		effective:        map[string]mathsdk.LegacyDec{"model-a": mathsdk.LegacyMustNewDecFromStr("1.25")},
		effectiveDecimal: map[string]*types.Decimal{"model-a": {Value: 125, Exponent: -2}},
		snapshot:         &types.DynamicCoefficientEpochSnapshot{},
	}

	scales := buildConfirmationWeightScales(
		[]string{"model-a"},
		participants,
		result,
	)
	require.Equal(t, []*types.ConfirmationWeightScale{{
		ModelId:              "model-a",
		EffectiveCoefficient: &types.Decimal{Value: 125, Exponent: -2},
	}}, scales)
}
