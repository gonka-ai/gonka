package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestModelCoefficients(t *testing.T) {
	t.Run("nil params", func(t *testing.T) {
		coeffs := modelCoefficients(nil)
		require.Empty(t, coeffs)
	})

	t.Run("extracts weight scale factors", func(t *testing.T) {
		params := &types.PocParams{
			Models: []*types.PoCModelConfig{
				{ModelId: "model-a", WeightScaleFactor: &types.Decimal{Value: 1, Exponent: 0}},
				{ModelId: "model-b", WeightScaleFactor: &types.Decimal{Value: 2, Exponent: 0}},
			},
		}
		coeffs := modelCoefficients(params)
		require.Len(t, coeffs, 2)
		require.True(t, coeffs["model-a"].Equal(mathsdk.LegacyOneDec()))
		require.True(t, coeffs["model-b"].Equal(mathsdk.LegacyNewDec(2)))
	})
}
