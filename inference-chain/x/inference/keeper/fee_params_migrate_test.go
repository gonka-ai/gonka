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
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos}, updated.FeeParams.EnabledFeeGroups)
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
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos}, updated.FeeParams.EnabledFeeGroups)
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

func TestMigrateFeeParamsToTree_ClampsUncappedLegacyRates(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = &types.FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: types.MaxPeriodBaseGas + 1,
		GasPerPocCount:    types.MaxGasPerUnit + 1,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.MigrateFeeParamsToTree(ctx))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, updated.FeeParams.Validate())
	_, rule := updated.FeeParams.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	require.NotNil(t, rule)
	require.Equal(t, types.MaxPeriodBaseGas, rule.Base.Gas)
	require.Equal(t, types.MaxGasPerUnit, rule.GetStoredDelta().GasPerUnit)
	require.Equal(t, types.MaxPeriodBaseGas, updated.FeeParams.BaseValidationGas)
	require.Equal(t, types.MaxGasPerUnit, updated.FeeParams.GasPerPocCount)
}

func TestMigrateFeeParamsToTree_ExistingTree(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	fp.Groups = fp.Groups[:1]
	fp.EnabledFeeGroups = nil
	fp.Groups[0].MinGasPrice = 17
	fp.Groups[0].Msgs[0].GetStoredDelta().GasPerUnit = 321
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.MigrateFeeParamsToTree(ctx))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, updated.FeeParams.Validate())
	require.Equal(t, []string{"epoch", "cosmos"}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName("epoch").MinGasPrice)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName("cosmos").MinGasPrice)
	require.Equal(t, uint64(321), updated.FeeParams.Groups[0].Msgs[0].GetStoredDelta().GasPerUnit)
}
