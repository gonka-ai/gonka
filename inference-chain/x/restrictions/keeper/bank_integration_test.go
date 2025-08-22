package keeper_test

import (
	"context"
	"fmt"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	restrictionskeeper "github.com/productscience/inference/x/restrictions/keeper"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

// MockBankKeeper implements a simple bank keeper for integration testing
type MockBankKeeper struct {
	balances map[string]sdk.Coins
	supplies sdk.Coins
}

func NewMockBankKeeper() *MockBankKeeper {
	return &MockBankKeeper{
		balances: make(map[string]sdk.Coins),
		supplies: sdk.NewCoins(),
	}
}

func (m *MockBankKeeper) SpendableCoins(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	if coins, exists := m.balances[addr.String()]; exists {
		return coins
	}
	return sdk.NewCoins()
}

func (m *MockBankKeeper) SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error {
	// Simple implementation - just move coins between balances
	fromKey := fromAddr.String()
	toKey := toAddr.String()

	// Check if sender has enough coins
	fromBalance := m.balances[fromKey]
	if !fromBalance.IsAllGTE(amt) {
		return fmt.Errorf("insufficient funds: %s < %s", fromBalance, amt)
	}

	// Subtract from sender
	newFromBalance := fromBalance.Sub(amt...)
	if newFromBalance.IsZero() {
		delete(m.balances, fromKey)
	} else {
		m.balances[fromKey] = newFromBalance
	}

	// Add to receiver
	if toBalance, exists := m.balances[toKey]; exists {
		m.balances[toKey] = toBalance.Add(amt...)
	} else {
		m.balances[toKey] = amt
	}

	return nil
}

// MockAccountKeeper implements a simple account keeper for integration testing
type MockAccountKeeper struct {
	accounts map[string]sdk.AccountI
}

func (m *MockAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	addrStr := addr.String()
	if account, exists := m.accounts[addrStr]; exists {
		return account
	}

	// For testing purposes, check if this is a known module address
	knownModules := []string{
		"fee_collector", "distribution", "mint", "bonded_tokens_pool", "not_bonded_tokens_pool", "gov",
		"inference", "streamvesting", "collateral", "bookkeeper", "bls", "genesistransfer", "restrictions",
		"top_reward", "pre_programmed_sale", // Special accounts
	}

	for _, moduleName := range knownModules {
		moduleAddr := authtypes.NewModuleAddress(moduleName)
		if addr.Equals(moduleAddr) {
			// Create and cache a mock module account
			moduleAccount := &authtypes.ModuleAccount{
				BaseAccount: &authtypes.BaseAccount{Address: addrStr},
				Name:        moduleName,
			}
			m.accounts[addrStr] = moduleAccount
			return moduleAccount
		}
	}

	// Create and cache a regular account for non-module addresses
	baseAccount := &authtypes.BaseAccount{Address: addrStr}
	m.accounts[addrStr] = baseAccount
	return baseAccount
}

func (m *MockBankKeeper) SetBalance(addr sdk.AccAddress, coins sdk.Coins) {
	m.balances[addr.String()] = coins
}

func (m *MockBankKeeper) GetBalance(addr sdk.AccAddress) sdk.Coins {
	if coins, exists := m.balances[addr.String()]; exists {
		return coins
	}
	return sdk.NewCoins()
}

// setupIntegrationKeepers creates real restrictions keeper with mock bank keeper for integration testing
func setupIntegrationKeepers(t testing.TB) (sdk.Context, restrictionskeeper.Keeper, *MockBankKeeper, restrictionstypes.MsgServer) {
	// --- Store and Codec Setup ---
	restrictionsStoreKey := storetypes.NewKVStoreKey(restrictionstypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(restrictionsStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	// --- Mock Bank Keeper ---
	bankKeeper := NewMockBankKeeper()

	// --- Mock Account Keeper ---
	accountKeeper := &MockAccountKeeper{accounts: make(map[string]sdk.AccountI)}

	// --- Real Restrictions Keeper ---
	restrictionsKeeper := restrictionskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(restrictionsStoreKey),
		log.NewNopLogger(),
		authority.String(),
		accountKeeper,
		bankKeeper,
	)

	// Initialize default params
	require.NoError(t, restrictionsKeeper.SetParams(ctx, restrictionstypes.DefaultParams()))

	msgServer := restrictionskeeper.NewMsgServerImpl(restrictionsKeeper)

	return ctx, restrictionsKeeper, bankKeeper, msgServer
}

func TestBankIntegration_SendRestrictionBlocksUserToUserTransfers(t *testing.T) {
	ctx, keeper, bankKeeper, _ := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block to ensure restrictions are active
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height to be before restriction end
	ctx = ctx.WithBlockHeight(100000)

	// Create test addresses
	fromAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	toAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up initial balances
	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(fromAddr, initialAmount)

	// Amount to transfer
	transferAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))

	// Test SendRestriction directly - should block user-to-user transfer
	_, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), fromAddr, toAddr, transferAmount)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer restricted")

	// Verify balances unchanged (restriction blocked the transfer)
	require.Equal(t, initialAmount, bankKeeper.GetBalance(fromAddr))
	require.True(t, bankKeeper.GetBalance(toAddr).IsZero())
}

func TestBankIntegration_SendRestrictionAllowsGasFeePayments(t *testing.T) {
	ctx, keeper, bankKeeper, _ := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height to be before restriction end
	ctx = ctx.WithBlockHeight(100000)

	// Create test addresses
	fromAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	feeCollectorAddr := authtypes.NewModuleAddress(authtypes.FeeCollectorName)

	// Set up initial balances
	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(fromAddr, initialAmount)

	// Gas fee amount
	gasAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(50)))

	// Test SendRestriction for gas fee payment - should allow
	newToAddr, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), fromAddr, feeCollectorAddr, gasAmount)
	require.NoError(t, err)
	require.Equal(t, feeCollectorAddr, newToAddr)

	// Actually execute the transfer to verify it works
	err = bankKeeper.SendCoins(sdk.WrapSDKContext(ctx), fromAddr, feeCollectorAddr, gasAmount)
	require.NoError(t, err)

	// Verify balances updated correctly
	expectedFromBalance := initialAmount.Sub(gasAmount...)
	require.Equal(t, expectedFromBalance, bankKeeper.GetBalance(fromAddr))
	require.Equal(t, gasAmount, bankKeeper.GetBalance(feeCollectorAddr))
}

func TestBankIntegration_SendRestrictionAllowsModuleTransfers(t *testing.T) {
	ctx, keeper, bankKeeper, _ := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height to be before restriction end
	ctx = ctx.WithBlockHeight(100000)

	// Test user-to-module transfer (allowed)
	userAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	inferenceModuleAddr := authtypes.NewModuleAddress("inference")

	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(userAddr, initialAmount)

	transferAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300)))

	// Should allow user-to-module transfer
	newToAddr, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, inferenceModuleAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, inferenceModuleAddr, newToAddr)

	// Test module-to-user transfer (allowed)
	moduleAddr := authtypes.NewModuleAddress("gov")
	userAddr2, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)

	bankKeeper.SetBalance(moduleAddr, sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500))))

	// Should allow module-to-user transfer
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), moduleAddr, userAddr2, transferAmount)
	require.NoError(t, err)
	require.Equal(t, userAddr2, newToAddr)
}

func TestBankIntegration_EmergencyTransferExecution(t *testing.T) {
	ctx, keeper, bankKeeper, msgServer := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create emergency exemption
	exemption := restrictionstypes.EmergencyTransferExemption{
		ExemptionId:   "emergency-test",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    3,
		ExpiryBlock:   1500000,
		Justification: "Integration test emergency transfer",
	}
	params.EmergencyTransferExemptions = []restrictionstypes.EmergencyTransferExemption{exemption}

	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height
	ctx = ctx.WithBlockHeight(100000)

	// Set up initial balances
	fromAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	toAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(2000)))
	bankKeeper.SetBalance(fromAddr, initialAmount)

	// Execute emergency transfer
	msg := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "emergency-test",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	resp, err := msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(2), resp.RemainingUses) // 3 - 1 = 2

	// Verify balances updated
	transferAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))
	expectedFromBalance := initialAmount.Sub(transferAmount...)
	require.Equal(t, expectedFromBalance, bankKeeper.GetBalance(fromAddr))
	require.Equal(t, transferAmount, bankKeeper.GetBalance(toAddr))

	// Verify usage tracking was updated
	updatedParams := keeper.GetParams(ctx)
	require.Len(t, updatedParams.ExemptionUsageTracking, 1)
	require.Equal(t, "emergency-test", updatedParams.ExemptionUsageTracking[0].ExemptionId)
	require.Equal(t, testutil.Creator, updatedParams.ExemptionUsageTracking[0].AccountAddress)
	require.Equal(t, uint64(1), updatedParams.ExemptionUsageTracking[0].UsageCount)
}

func TestBankIntegration_RestrictionLifecycleWithAutoUnregistration(t *testing.T) {
	ctx, keeper, bankKeeper, _ := setupIntegrationKeepers(t)

	// Set up restrictions that will expire soon
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 1000 // Will expire at block 1000
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Create test addresses
	fromAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	toAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(fromAddr, initialAmount)
	transferAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))

	// Test restrictions are active before expiry
	ctx = ctx.WithBlockHeight(500) // Before expiry
	_, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), fromAddr, toAddr, transferAmount)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer restricted")

	// Test restrictions become inactive after expiry
	ctx = ctx.WithBlockHeight(1500) // After expiry
	newToAddr, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), fromAddr, toAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, toAddr, newToAddr)

	// Test auto-unregistration logic
	keeper.CheckAndUnregisterRestriction(ctx)

	// Verify that subsequent calls still work (idempotent)
	keeper.CheckAndUnregisterRestriction(ctx)
}

func TestBankIntegration_MultipleEmergencyTransfersWithLimits(t *testing.T) {
	ctx, keeper, bankKeeper, msgServer := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create emergency exemption with limited usage
	exemption := restrictionstypes.EmergencyTransferExemption{
		ExemptionId:   "limited-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "300",
		UsageLimit:    2, // Only 2 uses allowed
		ExpiryBlock:   1500000,
		Justification: "Limited usage test",
	}
	params.EmergencyTransferExemptions = []restrictionstypes.EmergencyTransferExemption{exemption}

	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height
	ctx = ctx.WithBlockHeight(100000)

	// Set up initial balances
	fromAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	toAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(2000)))
	bankKeeper.SetBalance(fromAddr, initialAmount)

	// First emergency transfer - should succeed
	msg1 := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "limited-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "200",
		Denom:       "ugonka",
	}

	resp1, err := msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp1.RemainingUses) // 2 - 1 = 1

	// Second emergency transfer - should succeed
	msg2 := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "limited-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "150",
		Denom:       "ugonka",
	}

	resp2, err := msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg2)
	require.NoError(t, err)
	require.Equal(t, uint64(0), resp2.RemainingUses) // 1 - 1 = 0

	// Third emergency transfer - should fail (usage limit exceeded)
	msg3 := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "limited-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "100",
		Denom:       "ugonka",
	}

	_, err = msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage limit exceeded")

	// Verify final balances
	transferredTotal := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(350))) // 200 + 150
	expectedFromBalance := initialAmount.Sub(transferredTotal...)
	require.Equal(t, expectedFromBalance, bankKeeper.GetBalance(fromAddr))
	require.Equal(t, transferredTotal, bankKeeper.GetBalance(toAddr))
}

func TestBankIntegration_WildcardExemptions(t *testing.T) {
	ctx, keeper, bankKeeper, msgServer := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create wildcard exemption (any sender to specific receiver)
	exemption := restrictionstypes.EmergencyTransferExemption{
		ExemptionId:   "wildcard-from",
		FromAddress:   "*", // Wildcard sender
		ToAddress:     testutil.Requester,
		MaxAmount:     "500",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Wildcard sender test",
	}
	params.EmergencyTransferExemptions = []restrictionstypes.EmergencyTransferExemption{exemption}

	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height
	ctx = ctx.WithBlockHeight(100000)

	// Set up balances for multiple senders
	sender1, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	sender2, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	toAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	initialAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(sender1, initialAmount)
	bankKeeper.SetBalance(sender2, initialAmount)

	// Test transfer from first sender using wildcard exemption
	msg1 := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "wildcard-from",
		FromAddress: testutil.Creator, // Different from exemption's "*" but should match
		ToAddress:   testutil.Requester,
		Amount:      "300",
		Denom:       "ugonka",
	}

	resp1, err := msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg1)
	require.NoError(t, err)
	require.Equal(t, uint64(4), resp1.RemainingUses) // 5 - 1 = 4

	// Test transfer from second sender using same wildcard exemption
	msg2 := &restrictionstypes.MsgExecuteEmergencyTransfer{
		ExemptionId: "wildcard-from",
		FromAddress: testutil.Executor, // Different sender, same exemption
		ToAddress:   testutil.Requester,
		Amount:      "200",
		Denom:       "ugonka",
	}

	resp2, err := msgServer.ExecuteEmergencyTransfer(sdk.WrapSDKContext(ctx), msg2)
	require.NoError(t, err)
	require.Equal(t, uint64(4), resp2.RemainingUses) // 5 - 1 = 4 (this is executor's first use)

	// Verify both transfers succeeded
	transfer1Amount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300)))
	transfer2Amount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(200)))
	totalReceived := transfer1Amount.Add(transfer2Amount...)

	require.Equal(t, initialAmount.Sub(transfer1Amount...), bankKeeper.GetBalance(sender1))
	require.Equal(t, initialAmount.Sub(transfer2Amount...), bankKeeper.GetBalance(sender2))
	require.Equal(t, totalReceived, bankKeeper.GetBalance(toAddr))

	// Verify usage tracking for both senders
	updatedParams := keeper.GetParams(ctx)
	require.Len(t, updatedParams.ExemptionUsageTracking, 2)

	// Usage should be tracked separately per account
	var creator1Usage, executor1Usage *restrictionstypes.ExemptionUsage
	for i := range updatedParams.ExemptionUsageTracking {
		usage := &updatedParams.ExemptionUsageTracking[i]
		if usage.AccountAddress == testutil.Creator {
			creator1Usage = usage
		} else if usage.AccountAddress == testutil.Executor {
			executor1Usage = usage
		}
	}

	require.NotNil(t, creator1Usage)
	require.NotNil(t, executor1Usage)
	require.Equal(t, uint64(1), creator1Usage.UsageCount)
	require.Equal(t, uint64(1), executor1Usage.UsageCount)
}

func TestBankIntegration_CrossModuleTransferScenarios(t *testing.T) {
	ctx, keeper, bankKeeper, _ := setupIntegrationKeepers(t)

	// Set up active restrictions
	params := restrictionstypes.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height
	ctx = ctx.WithBlockHeight(100000)

	// Test various module account scenarios
	userAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)

	// Common test amount
	transferAmount := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(100)))
	initialBalance := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
	bankKeeper.SetBalance(userAddr, initialBalance)

	// Test inference module transfers (should be allowed)
	inferenceAddr := authtypes.NewModuleAddress("inference")
	bankKeeper.SetBalance(inferenceAddr, initialBalance)

	// User to inference module (escrow payment)
	newToAddr, err := keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, inferenceAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, inferenceAddr, newToAddr)

	// Inference module to user (reward payment)
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), inferenceAddr, userAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, userAddr, newToAddr)

	// Test streamvesting module transfers (should be allowed)
	streamvestingAddr := authtypes.NewModuleAddress("streamvesting")
	bankKeeper.SetBalance(streamvestingAddr, initialBalance)

	// Streamvesting to user (vested reward release)
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), streamvestingAddr, userAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, userAddr, newToAddr)

	// Test governance module transfers (should be allowed)
	govAddr := authtypes.NewModuleAddress("gov")
	bankKeeper.SetBalance(govAddr, initialBalance)

	// User to governance (proposal deposit)
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), userAddr, govAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, govAddr, newToAddr)

	// Governance to user (deposit refund)
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), govAddr, userAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, userAddr, newToAddr)

	// Test distribution module transfers (should be allowed)
	distributionAddr := authtypes.NewModuleAddress("distribution")
	bankKeeper.SetBalance(distributionAddr, initialBalance)

	// Distribution to user (staking rewards)
	newToAddr, err = keeper.SendRestrictionFn(sdk.WrapSDKContext(ctx), distributionAddr, userAddr, transferAmount)
	require.NoError(t, err)
	require.Equal(t, userAddr, newToAddr)
}
