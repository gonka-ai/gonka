package inference

import (
	"testing"

	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBuildConfirmationWeightScalesUsesRealNodesAndKeepsControllerState(t *testing.T) {
	participants := []*types.ActiveParticipant{{
		Index:  "host-a",
		Models: []string{"model-a", "ineligible"},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 10}}},
			{MlNodes: []*types.MLNodeInfo{{PocWeight: 20}}},
		},
		// No voting power: weightcap can leave a new host with zero trust
		// power while its real PoC nodes remain confirmable.
	}}
	result := &coefficient.Result{
		Scales: []*types.ConfirmationWeightScale{
			{
				ModelId:              "model-a",
				EffectiveCoefficient: &types.Decimal{Value: 125, Exponent: -2},
				BaseCoefficient:      &types.Decimal{Value: 150, Exponent: -2},
				AdaptiveStep:         &types.Decimal{Value: 5, Exponent: -2},
				PrevSign:             1,
			},
			{
				ModelId:              "model-b",
				EffectiveCoefficient: &types.Decimal{Value: 2, Exponent: 0},
			},
			{
				ModelId:              "ineligible",
				EffectiveCoefficient: &types.Decimal{Value: 3, Exponent: 0},
			},
		},
	}

	scales := buildConfirmationWeightScales(
		[]string{"model-a", "model-b"},
		participants,
		result,
	)

	require.Equal(t, []*types.ConfirmationWeightScale{
		{
			ModelId:              "ineligible",
			EffectiveCoefficient: &types.Decimal{Value: 3, Exponent: 0},
			// Every coefficient scale remains persisted for controller state,
			// but ineligible models cannot participate in confirmation.
			ExcludeFromConfirmation: true,
		},
		{
			ModelId:              "model-a",
			EffectiveCoefficient: &types.Decimal{Value: 125, Exponent: -2},
			BaseCoefficient:      &types.Decimal{Value: 150, Exponent: -2},
			AdaptiveStep:         &types.Decimal{Value: 5, Exponent: -2},
			PrevSign:             1,
		},
		{
			ModelId:                 "model-b",
			EffectiveCoefficient:    &types.Decimal{Value: 2, Exponent: 0},
			ExcludeFromConfirmation: true,
		},
	}, scales)
}
