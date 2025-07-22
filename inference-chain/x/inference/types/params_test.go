package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDefaultParams(t *testing.T) {
	params := types.DefaultParams()

	// Test default values for pruning parameters
	require.Equal(t, uint64(2), params.EpochParams.InferencePruningEpochThreshold)
	require.Equal(t, uint64(1), params.PocParams.PocDataPruningEpochThreshold)
}

func TestParamsValidation(t *testing.T) {
	params := types.DefaultParams()

	// Test that the default params are valid
	require.NoError(t, params.Validate())

	// Test that params are still valid with different pruning thresholds
	params.EpochParams.InferencePruningEpochThreshold = 5
	params.PocParams.PocDataPruningEpochThreshold = 3
	require.NoError(t, params.Validate())
}
