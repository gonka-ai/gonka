package keeper_test

import (
	"testing"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestFeeParams_NilAtGenesis(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.FeeParams)
	require.Empty(t, params.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(0), params.FeeParams.MinGasPriceNgonka)
}

func TestFeeParams_SetAndGet(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)

	params.FeeParams = &types.FeeParams{
		MinGasPriceNgonka: 42,
		BaseValidationGas: 1_000_000,
		GasPerPocCount:    200,
	}
	require.NoError(t, k.SetParams(ctx, params))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, updated.FeeParams)
	require.Equal(t, uint64(42), updated.FeeParams.MinGasPriceNgonka)
	require.Equal(t, uint64(1_000_000), updated.FeeParams.BaseValidationGas)
	require.Equal(t, uint64(200), updated.FeeParams.GasPerPocCount)
}

func TestFeeParams_ZeroDisablesFees(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	// Enable fees
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = types.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), updated.FeeParams.MinGasPriceNgonka)

	// Disable by setting to zero
	params.FeeParams = &types.FeeParams{}
	require.NoError(t, k.SetParams(ctx, params))

	updated, err = k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), updated.FeeParams.MinGasPriceNgonka)
}
