package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestMigrateFeeParamsToTree_Nil(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.MigrateFeeParamsToTree(ctx))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, updated.FeeParams)
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(0), updated.FeeParams.MinGasPriceNgonka)
	require.NotEmpty(t, updated.FeeParams.Groups)
}

func TestMigrateFeeParamsToTree_CopiesFlatRates(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = &types.FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: 777_000,
		GasPerPocCount:    33,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.MigrateFeeParamsToTree(ctx))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)
	_, rule := updated.FeeParams.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	require.NotNil(t, rule)
	require.Equal(t, uint64(777_000), rule.Base.Gas)
	require.Equal(t, uint64(33), rule.GetStoredDelta().GasPerUnit)
}

func TestMigrateFeeParamsToTree_CopiesExplicitZeros(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = &types.FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: 0,
		GasPerPocCount:    0,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.MigrateFeeParamsToTree(ctx))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	_, rule := updated.FeeParams.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	require.NotNil(t, rule)
	require.Equal(t, uint64(0), rule.Base.Gas, "legacy zero base must not become 500k")
	require.Equal(t, uint64(0), rule.GetStoredDelta().GasPerUnit, "legacy zero rate must not become 100")
	require.Equal(t, uint64(0), updated.FeeParams.BaseValidationGas)
	require.Equal(t, uint64(0), updated.FeeParams.GasPerPocCount)
}
