package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

func TestTransferRestrictionFunction_RestrictionsInactive(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set restrictions to be inactive (current block > restriction end)
	params := types.DefaultParams()
	params.RestrictionEndBlock = 100 // Past block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(200) // Current block after restriction end

	// Create test addresses
	from := sdk.AccAddress("from_address_______")
	to := sdk.AccAddress("to_address_________")
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// When restrictions are inactive, all transfers should be allowed
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), from, to, amt)
	require.NoError(t, err)
	require.Equal(t, to, newTo)
}

func TestTransferRestrictionFunction_GasFeePayment(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// Create test addresses
	from := sdk.AccAddress("user_address_______")
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// Gas fee payments should be allowed
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), from, feeCollector, amt)
	require.NoError(t, err)
	require.Equal(t, feeCollector, newTo)
}

func TestTransferRestrictionFunction_UserToModuleTransfer(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// Create test addresses
	from := sdk.AccAddress("user_address_______")
	inferenceModule := authtypes.NewModuleAddress("inference")
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// User-to-module transfers should be allowed (e.g., inference escrow)
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), from, inferenceModule, amt)
	require.NoError(t, err)
	require.Equal(t, inferenceModule, newTo)
}

func TestTransferRestrictionFunction_ModuleToUserTransfer(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// Create test addresses
	inferenceModule := authtypes.NewModuleAddress("inference")
	to := sdk.AccAddress("user_address_______")
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// Module-to-user transfers should be allowed (e.g., rewards)
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), inferenceModule, to, amt)
	require.NoError(t, err)
	require.Equal(t, to, newTo)
}

func TestTransferRestrictionFunction_UserToUserRestricted(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// Create test addresses
	from := sdk.AccAddress("user1_address______")
	to := sdk.AccAddress("user2_address______")
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// User-to-user transfers should be restricted
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), from, to, amt)
	require.Error(t, err)
	require.Contains(t, err.Error(), "user-to-user transfers are restricted")
	require.Contains(t, err.Error(), "bootstrap period")
	require.Equal(t, to, newTo) // newTo should still be returned even on error
}

func TestTransferRestrictionFunction_EmergencyExemption(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set active restrictions with emergency exemption
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{
		{
			ExemptionId:   "emergency1",
			FromAddress:   "cosmos1testuser",
			ToAddress:     "*", // Any destination
			MaxAmount:     "5000",
			UsageLimit:    2,
			ExpiryBlock:   2500000, // Future expiry
			Justification: "Emergency test",
		},
	}
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// Create test addresses (note: these won't exactly match the exemption from address, so this will fail)
	from := sdk.AccAddress("user1_address______")
	to := sdk.AccAddress("user2_address______")
	amt := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	// This should still be restricted because the from address doesn't match
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), from, to, amt)
	require.Error(t, err)
	require.Equal(t, to, newTo)
}

func TestIsRestrictionActive(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Test when restrictions are active
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000)
	require.True(t, keeper.IsRestrictionActive(ctx))

	// Test when restrictions are inactive
	ctx = ctx.WithBlockHeight(2500000)
	require.False(t, keeper.IsRestrictionActive(ctx))
}

func TestIsGasFeePayment(t *testing.T) {
	keeper, _ := keepertest.RestrictionsKeeper(t)

	// Fee collector should be detected
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	require.True(t, keeper.IsGasFeePayment(feeCollector))

	// Regular address should not be detected as fee collector
	regular := sdk.AccAddress("regular_user_addr___")
	require.False(t, keeper.IsGasFeePayment(regular))
}

func TestIsModuleAccount(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Known module accounts should be detected
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	require.True(t, keeper.IsModuleAccount(ctx, feeCollector))

	inference := authtypes.NewModuleAddress("inference")
	require.True(t, keeper.IsModuleAccount(ctx, inference))

	gov := authtypes.NewModuleAddress("gov")
	require.True(t, keeper.IsModuleAccount(ctx, gov))

	// Test special sub-accounts (the main focus of this enhancement)
	topReward := authtypes.NewModuleAddress("top_reward")
	require.True(t, keeper.IsModuleAccount(ctx, topReward))

	preProgrammedSale := authtypes.NewModuleAddress("pre_programmed_sale")
	require.True(t, keeper.IsModuleAccount(ctx, preProgrammedSale))

	// Test other important module accounts
	distribution := authtypes.NewModuleAddress("distribution")
	require.True(t, keeper.IsModuleAccount(ctx, distribution))

	streamvesting := authtypes.NewModuleAddress("streamvesting")
	require.True(t, keeper.IsModuleAccount(ctx, streamvesting))

	collateral := authtypes.NewModuleAddress("collateral")
	require.True(t, keeper.IsModuleAccount(ctx, collateral))

	// Regular address should not be detected as module account
	regular := sdk.AccAddress("regular_user_addr___")
	require.False(t, keeper.IsModuleAccount(ctx, regular))
}

func TestSpecialModuleAccountTransfers(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set up restriction parameters (active restrictions)
	params := types.DefaultParams()
	params.RestrictionEndBlock = uint64(ctx.BlockHeight()) + 1000
	keeper.SetParams(ctx, params)

	// Test addresses
	userAddr := sdk.AccAddress("user_address_test___")
	anotherUser := sdk.AccAddress("another_user_test___")

	// Special module-controlled accounts
	topRewardAddr := authtypes.NewModuleAddress("top_reward")
	preProgrammedSaleAddr := authtypes.NewModuleAddress("pre_programmed_sale")
	inferenceAddr := authtypes.NewModuleAddress("inference")

	testCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(100)))

	// Test 1: Transfers FROM special accounts should be allowed (module operations)
	newTo, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), topRewardAddr, userAddr, testCoins)
	require.NoError(t, err)
	require.Equal(t, userAddr, newTo)

	newTo, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), preProgrammedSaleAddr, userAddr, testCoins)
	require.NoError(t, err)
	require.Equal(t, userAddr, newTo)

	// Test 2: Transfers TO special accounts should be allowed (user-to-module)
	newTo, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, topRewardAddr, testCoins)
	require.NoError(t, err)
	require.Equal(t, topRewardAddr, newTo)

	newTo, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, preProgrammedSaleAddr, testCoins)
	require.NoError(t, err)
	require.Equal(t, preProgrammedSaleAddr, newTo)

	// Test 3: Module-to-module transfers should be allowed
	newTo, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), topRewardAddr, inferenceAddr, testCoins)
	require.NoError(t, err)
	require.Equal(t, inferenceAddr, newTo)

	// Test 4: User-to-user transfers should still be restricted
	_, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, anotherUser, testCoins)
	require.Error(t, err)
	require.Contains(t, err.Error(), "user-to-user transfers are restricted")

	// Test 5: Verify special accounts are properly detected
	require.True(t, keeper.IsModuleAccount(ctx, topRewardAddr))
	require.True(t, keeper.IsModuleAccount(ctx, preProgrammedSaleAddr))
	require.False(t, keeper.IsModuleAccount(ctx, userAddr))
}
