package v0_2_17

import (
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

func TestEnableWasmFeeGroup_AddsWasmBesideExistingGroups(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = types.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName(types.FeeGroupEpoch).MinGasPrice)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName(types.FeeGroupCosmos).MinGasPrice)
	require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
	require.NoError(t, updated.FeeParams.Validate())

	for _, msg := range []sdk.Msg{
		&wasmtypes.MsgStoreCode{},
		&wasmtypes.MsgInstantiateContract{},
		&wasmtypes.MsgExecuteContract{},
		&wasmtypes.MsgMigrateContract{},
	} {
		require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.EnabledPayingPrice([]sdk.Msg{msg}, types.IsNetworkDuty))
	}
	require.Equal(t, uint64(1), updated.FeeParams.EnabledPayingPrice([]sdk.Msg{&types.MsgPoCV2StoreCommit{}}, types.IsNetworkDuty))
}

func TestEnableWasmFeeGroup_IdempotentAndKeepsExistingPrice(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	fp.Groups = append(fp.Groups, &types.FeeGroup{Name: types.FeeGroupWasm, MinGasPrice: 4})
	fp.EnabledFeeGroups = append(fp.EnabledFeeGroups, types.FeeGroupWasm)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))
	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(4), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
}

func TestEnableWasmFeeGroup_NilParamsGetsDefaultsPlusWasm(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
	require.NoError(t, updated.FeeParams.Validate())
}
