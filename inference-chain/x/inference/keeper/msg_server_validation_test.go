package keeper_test

import (
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZScoreCalculator(t *testing.T) {
	// Separately calculate values to confirm results
	equal := keeper.CalculateZScoreFromFPR(0.05, 95, 5)
	require.Equal(t, 0.0, equal)

	negative := keeper.CalculateZScoreFromFPR(0.05, 96, 4)
	require.InDelta(t, -0.458831, negative, 0.00001)

	positive := keeper.CalculateZScoreFromFPR(0.05, 94, 6)
	require.InDelta(t, 0.458831, positive, 0.00001)

	bigNegative := keeper.CalculateZScoreFromFPR(0.05, 960, 40)
	require.InDelta(t, -1.450953, bigNegative, 0.00001)

	bigPositive := keeper.CalculateZScoreFromFPR(0.05, 940, 60)
	require.InDelta(t, 1.450953, bigPositive, 0.00001)
}

func TestMeasurementsNeeded(t *testing.T) {
	require.Equal(t, uint64(53), keeper.MeasurementsNeeded(0.05))
	require.Equal(t, uint64(27), keeper.MeasurementsNeeded(0.10))
	require.Equal(t, uint64(262), keeper.MeasurementsNeeded(0.01))

}
