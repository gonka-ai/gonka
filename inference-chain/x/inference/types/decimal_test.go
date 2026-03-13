package types

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestDecimalFromLegacyDec(t *testing.T) {
	t.Run("converts fractional decimal", func(t *testing.T) {
		value, err := math.LegacyNewDecFromStr("0.6")
		require.NoError(t, err)

		converted, err := DecimalFromLegacyDec(value)
		require.NoError(t, err)
		require.Equal(t, "0.6", converted.ToDecimal().String())
	})

	t.Run("converts repeating decimal using chain string form", func(t *testing.T) {
		value, err := math.LegacyNewDecFromStr("0.333333333333333333")
		require.NoError(t, err)

		converted, err := DecimalFromLegacyDec(value)
		require.NoError(t, err)
		require.Equal(t, value.String(), converted.ToDecimal().String())
	})
}