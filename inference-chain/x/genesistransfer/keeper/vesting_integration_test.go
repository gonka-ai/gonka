package keeper_test

import (
	"context"
	"testing"
	"time"

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
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/genesistransfer/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

// IntegrationMockAccountKeeper implements a more complete account keeper for integration testing
type IntegrationMockAccountKeeper struct {
	accounts map[string]sdk.AccountI
}

func NewIntegrationMockAccountKeeper() *IntegrationMockAccountKeeper {
	return &IntegrationMockAccountKeeper{
		accounts: make(map[string]sdk.AccountI),
	}
}

func (m *IntegrationMockAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	if acc, exists := m.accounts[addr.String()]; exists {
		return acc
	}
	return nil
}

func (m *IntegrationMockAccountKeeper) SetAccount(ctx context.Context, acc sdk.AccountI) {
	if acc != nil {
		m.accounts[acc.GetAddress().String()] = acc
	}
}

func (m *IntegrationMockAccountKeeper) NewAccountWithAddress(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(addr)
}

// IntegrationMockBankKeeper implements a more complete bank keeper for integration testing
type IntegrationMockBankKeeper struct {
	balances map[string]sdk.Coins
}

func NewIntegrationMockBankKeeper() *IntegrationMockBankKeeper {
	return &IntegrationMockBankKeeper{
		balances: make(map[string]sdk.Coins),
	}
}

func (m *IntegrationMockBankKeeper) SpendableCoins(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	if coins, exists := m.balances[addr.String()]; exists {
		return coins
	}
	return sdk.NewCoins()
}

func (m *IntegrationMockBankKeeper) GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	if coins, exists := m.balances[addr.String()]; exists {
		return coins
	}
	return sdk.NewCoins()
}

func (m *IntegrationMockBankKeeper) SetBalance(addr sdk.AccAddress, coins sdk.Coins) {
	m.balances[addr.String()] = coins
}

// IntegrationMockBookkeepingBankKeeper implements bookkeeping bank operations
type IntegrationMockBookkeepingBankKeeper struct {
	bankKeeper *IntegrationMockBankKeeper
	logs       []string
}

func NewIntegrationMockBookkeepingBankKeeper(bankKeeper *IntegrationMockBankKeeper) *IntegrationMockBookkeepingBankKeeper {
	return &IntegrationMockBookkeepingBankKeeper{
		bankKeeper: bankKeeper,
		logs:       make([]string, 0),
	}
}

func (m *IntegrationMockBookkeepingBankKeeper) SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	// Log the transaction
	m.logs = append(m.logs, memo)

	// Execute the transfer
	fromKey := fromAddr.String()
	toKey := toAddr.String()

	// Check if sender has enough coins
	fromBalance := m.bankKeeper.balances[fromKey]
	if !fromBalance.IsAllGTE(amt) {
		return types.ErrInsufficientBalance.Wrapf("insufficient funds: %s < %s", fromBalance, amt)
	}

	// Subtract from sender
	newFromBalance := fromBalance.Sub(amt...)
	if newFromBalance.IsZero() {
		delete(m.bankKeeper.balances, fromKey)
	} else {
		m.bankKeeper.balances[fromKey] = newFromBalance
	}

	// Add to receiver
	if toBalance, exists := m.bankKeeper.balances[toKey]; exists {
		m.bankKeeper.balances[toKey] = toBalance.Add(amt...)
	} else {
		m.bankKeeper.balances[toKey] = amt
	}

	return nil
}

func (m *IntegrationMockBookkeepingBankKeeper) SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
	moduleAddr := authtypes.NewModuleAddress(recipientModule)
	return m.SendCoins(ctx, senderAddr, moduleAddr, amt, memo)
}

func (m *IntegrationMockBookkeepingBankKeeper) SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	moduleAddr := authtypes.NewModuleAddress(senderModule)
	return m.SendCoins(ctx, moduleAddr, recipientAddr, amt, memo)
}

// setupVestingIntegrationKeepers creates real genesistransfer keeper with enhanced mock dependencies
func setupVestingIntegrationKeepers(t testing.TB) (sdk.Context, keeper.Keeper, *IntegrationMockAccountKeeper, *IntegrationMockBankKeeper, *IntegrationMockBookkeepingBankKeeper, types.MsgServer) {
	// Store and Codec Setup
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	// Enhanced Mock Keepers
	accountKeeper := NewIntegrationMockAccountKeeper()
	bankKeeper := NewIntegrationMockBankKeeper()
	bookkeepingBankKeeper := NewIntegrationMockBookkeepingBankKeeper(bankKeeper)

	// Real GenesisTransfer Keeper
	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		log.NewNopLogger(),
		authority.String(),
		accountKeeper,
		bankKeeper,
		bookkeepingBankKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{Height: 1}, false, log.NewNopLogger())

	// Initialize params
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	msgServer := keeper.NewMsgServerImpl(k)

	return ctx, k, accountKeeper, bankKeeper, bookkeepingBankKeeper, msgServer
}

// TestVestingIntegration_PeriodicVestingAccountTransfer tests complete periodic vesting account transfer
func TestVestingIntegration_PeriodicVestingAccountTransfer(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses using testutil constants (proper gonka bech32 format)
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up current time for vesting calculations
	currentTime := time.Now().Unix()
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Create periodic vesting account with multiple periods
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	periods := []vestingtypes.Period{
		{
			Length: 1800, // 30 minutes
			Amount: sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300))),
		},
		{
			Length: 1800, // 30 minutes
			Amount: sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(400))),
		},
		{
			Length: 1800, // 30 minutes
			Amount: sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300))),
		},
	}

	periodicVestingAcc, err := vestingtypes.NewPeriodicVestingAccount(baseAccount, vestingCoins, currentTime, periods)
	require.NoError(t, err)

	// Set the vesting account
	accountKeeper.SetAccount(ctx, periodicVestingAcc)

	// Set initial balances (vesting coins)
	bankKeeper.SetBalance(genesisAddr, vestingCoins)

	// Execute vesting schedule transfer
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err)

	// Verify recipient account was created and has the correct vesting schedule
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	require.NotNil(t, recipientAccount)

	// Check if recipient account is a vesting account
	recipientVestingAcc, isVesting := recipientAccount.(*vestingtypes.PeriodicVestingAccount)
	require.True(t, isVesting, "recipient should have a periodic vesting account")
	require.NotNil(t, recipientVestingAcc)

	// Verify vesting schedule preservation
	require.Equal(t, vestingCoins, recipientVestingAcc.OriginalVesting)
	require.Len(t, recipientVestingAcc.VestingPeriods, 3)

	// Verify timeline preservation
	require.Equal(t, periods, recipientVestingAcc.VestingPeriods)
}

// TestVestingIntegration_ContinuousVestingAccountTransfer tests continuous vesting account transfer
func TestVestingIntegration_ContinuousVestingAccountTransfer(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up timing for continuous vesting
	currentTime := time.Now().Unix()
	startTime := currentTime - 1800 // Started 30 minutes ago
	endTime := currentTime + 1800   // Ends in 30 minutes
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Create continuous vesting account
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	continuousVestingAcc, err := vestingtypes.NewContinuousVestingAccount(baseAccount, vestingCoins, startTime, endTime)
	require.NoError(t, err)

	// Set the vesting account
	accountKeeper.SetAccount(ctx, continuousVestingAcc)

	// Set initial balances
	bankKeeper.SetBalance(genesisAddr, vestingCoins)

	// Execute vesting schedule transfer
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err)

	// Verify recipient account was created and has the correct vesting schedule
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	require.NotNil(t, recipientAccount)

	// Check if recipient account is a continuous vesting account
	recipientVestingAcc, isVesting := recipientAccount.(*vestingtypes.ContinuousVestingAccount)
	require.True(t, isVesting, "recipient should have a continuous vesting account")
	require.NotNil(t, recipientVestingAcc)

	// Verify timeline preservation (should preserve original end time)
	require.Equal(t, endTime, recipientVestingAcc.EndTime)
	require.Equal(t, currentTime, recipientVestingAcc.StartTime) // Start time should be current time
}

// TestVestingIntegration_DelayedVestingAccountTransfer tests delayed vesting account transfer
func TestVestingIntegration_DelayedVestingAccountTransfer(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up timing for delayed vesting
	currentTime := time.Now().Unix()
	endTime := currentTime + 3600 // Ends in 1 hour
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Create delayed vesting account
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	delayedVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, endTime)
	require.NoError(t, err)

	// Set the vesting account
	accountKeeper.SetAccount(ctx, delayedVestingAcc)

	// Set initial balances
	bankKeeper.SetBalance(genesisAddr, vestingCoins)

	// Execute vesting schedule transfer
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err)

	// Verify recipient account was created and has the correct vesting schedule
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	require.NotNil(t, recipientAccount)

	// Check if recipient account is a delayed vesting account
	recipientVestingAcc, isVesting := recipientAccount.(*vestingtypes.DelayedVestingAccount)
	require.True(t, isVesting, "recipient should have a delayed vesting account")
	require.NotNil(t, recipientVestingAcc)

	// Verify timeline preservation
	require.Equal(t, endTime, recipientVestingAcc.EndTime)
	require.Equal(t, vestingCoins, recipientVestingAcc.OriginalVesting)
}

// TestVestingIntegration_CompleteOwnershipTransferWithVesting tests full ownership transfer including vesting
func TestVestingIntegration_CompleteOwnershipTransferWithVesting(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, msgServer := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up timing
	currentTime := time.Now().Unix()
	endTime := currentTime + 3600 // 1 hour vesting
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0)).WithBlockHeight(100)

	// Create genesis account with both liquid and vesting balances
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))
	liquidCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300)))
	totalCoins := vestingCoins.Add(liquidCoins...)

	// Create delayed vesting account
	delayedVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, endTime)
	require.NoError(t, err)

	// Set the genesis account
	accountKeeper.SetAccount(ctx, delayedVestingAcc)

	// Set initial balances (total = liquid + vesting)
	bankKeeper.SetBalance(genesisAddr, totalCoins)

	// Execute complete ownership transfer
	msg := &types.MsgTransferOwnership{
		GenesisAddress:   genesisAddr.String(),
		RecipientAddress: recipientAddr.String(),
	}

	resp, err := msgServer.TransferOwnership(sdk.WrapSDKContext(ctx), msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify recipient received all assets
	recipientBalance := bankKeeper.GetAllBalances(ctx, recipientAddr)
	require.Equal(t, totalCoins, recipientBalance)

	// Verify recipient has vesting account
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	require.NotNil(t, recipientAccount)

	recipientVestingAcc, isVesting := recipientAccount.(*vestingtypes.DelayedVestingAccount)
	require.True(t, isVesting, "recipient should have a delayed vesting account")
	require.Equal(t, vestingCoins, recipientVestingAcc.OriginalVesting)
	require.Equal(t, endTime, recipientVestingAcc.EndTime)

	// Verify transfer record was created
	transferRecord, found, err := k.GetTransferRecord(ctx, genesisAddr)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, transferRecord)
	require.Equal(t, genesisAddr.String(), transferRecord.GenesisAddress)
	require.Equal(t, recipientAddr.String(), transferRecord.RecipientAddress)
	require.True(t, transferRecord.Completed)
	require.Equal(t, uint64(100), transferRecord.TransferHeight)

	// Verify transfer cannot be executed again (one-time enforcement)
	_, err = msgServer.TransferOwnership(sdk.WrapSDKContext(ctx), msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already been transferred")
}

// TestVestingIntegration_NonVestingAccountHandling tests handling of non-vesting accounts
func TestVestingIntegration_NonVestingAccountHandling(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Create regular (non-vesting) account
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	accountKeeper.SetAccount(ctx, baseAccount)

	// Set liquid balances only
	liquidCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))
	bankKeeper.SetBalance(genesisAddr, liquidCoins)

	// Execute vesting schedule transfer (should handle gracefully)
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err) // Should not error for non-vesting accounts

	// Verify no vesting account was created for recipient (since source had no vesting)
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	if recipientAccount != nil {
		// If recipient account exists, it should not be a vesting account
		// Check for any of the vesting account types
		_, isPeriodicVesting := recipientAccount.(*vestingtypes.PeriodicVestingAccount)
		_, isContinuousVesting := recipientAccount.(*vestingtypes.ContinuousVestingAccount)
		_, isDelayedVesting := recipientAccount.(*vestingtypes.DelayedVestingAccount)
		_, isBaseVesting := recipientAccount.(*vestingtypes.BaseVestingAccount)

		isVesting := isPeriodicVesting || isContinuousVesting || isDelayedVesting || isBaseVesting
		require.False(t, isVesting, "recipient should not have a vesting account for non-vesting source")
	}
}

// TestVestingIntegration_ExpiredVestingAccountHandling tests handling of expired vesting accounts
func TestVestingIntegration_ExpiredVestingAccountHandling(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up expired vesting timing
	currentTime := time.Now().Unix()
	pastEndTime := currentTime - 3600 // Ended 1 hour ago
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Create expired delayed vesting account
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	delayedVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, pastEndTime)
	require.NoError(t, err)

	// Set the vesting account
	accountKeeper.SetAccount(ctx, delayedVestingAcc)

	// Set balances (all should be liquid now since vesting expired)
	bankKeeper.SetBalance(genesisAddr, vestingCoins)

	// Execute vesting schedule transfer (should handle expired vesting gracefully)
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err) // Should not error but should skip vesting transfer

	// Verify no vesting account was created for recipient (since vesting expired)
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	if recipientAccount != nil {
		// If recipient account exists, it should not be a vesting account
		// Check for any of the vesting account types
		_, isPeriodicVesting := recipientAccount.(*vestingtypes.PeriodicVestingAccount)
		_, isContinuousVesting := recipientAccount.(*vestingtypes.ContinuousVestingAccount)
		_, isDelayedVesting := recipientAccount.(*vestingtypes.DelayedVestingAccount)
		_, isBaseVesting := recipientAccount.(*vestingtypes.BaseVestingAccount)

		isVesting := isPeriodicVesting || isContinuousVesting || isDelayedVesting || isBaseVesting
		require.False(t, isVesting, "recipient should not have a vesting account for expired vesting")
	}
}

// TestVestingIntegration_WhitelistEnforcementWithVesting tests whitelist enforcement during vesting transfers
func TestVestingIntegration_WhitelistEnforcementWithVesting(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, msgServer := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up whitelist with genesis address allowed
	params := types.NewParams([]string{genesisAddr.String()}, true)
	err = k.SetParams(ctx, params)
	require.NoError(t, err)

	// Set up vesting account
	currentTime := time.Now().Unix()
	endTime := currentTime + 3600
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0)).WithBlockHeight(100)

	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))

	delayedVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, endTime)
	require.NoError(t, err)

	accountKeeper.SetAccount(ctx, delayedVestingAcc)
	bankKeeper.SetBalance(genesisAddr, vestingCoins)

	// Execute ownership transfer (should succeed - genesis address is whitelisted)
	msg := &types.MsgTransferOwnership{
		GenesisAddress:   genesisAddr.String(),
		RecipientAddress: recipientAddr.String(),
	}

	resp, err := msgServer.TransferOwnership(sdk.WrapSDKContext(ctx), msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify transfer completed successfully
	transferRecord, found, err := k.GetTransferRecord(ctx, genesisAddr)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, transferRecord.Completed)

	// Test with non-whitelisted address (should fail)
	nonWhitelistedAddr, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)

	// Create another vesting account for testing
	baseAccount2 := authtypes.NewBaseAccountWithAddress(nonWhitelistedAddr)
	delayedVestingAcc2, err := vestingtypes.NewDelayedVestingAccount(baseAccount2, vestingCoins, endTime)
	require.NoError(t, err)

	accountKeeper.SetAccount(ctx, delayedVestingAcc2)
	bankKeeper.SetBalance(nonWhitelistedAddr, vestingCoins)

	// Try to transfer non-whitelisted account (should fail)
	msg2 := &types.MsgTransferOwnership{
		GenesisAddress:   nonWhitelistedAddr.String(),
		RecipientAddress: recipientAddr.String(),
	}

	_, err = msgServer.TransferOwnership(sdk.WrapSDKContext(ctx), msg2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the allowed accounts whitelist")
}

// TestVestingIntegration_VestingTimelinePreservation tests that vesting timelines are preserved correctly
func TestVestingIntegration_VestingTimelinePreservation(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	// Create test addresses
	genesisAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	recipientAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)

	// Set up specific timing for timeline testing
	currentTime := time.Now().Unix()
	startTime := currentTime - 900 // Started 15 minutes ago
	endTime := currentTime + 2700  // Ends in 45 minutes (total 1 hour vesting)
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Create continuous vesting account
	baseAccount := authtypes.NewBaseAccountWithAddress(genesisAddr)
	originalVestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1200)))

	continuousVestingAcc, err := vestingtypes.NewContinuousVestingAccount(baseAccount, originalVestingCoins, startTime, endTime)
	require.NoError(t, err)

	// Set the vesting account
	accountKeeper.SetAccount(ctx, continuousVestingAcc)

	// Calculate what should be remaining (proportional to remaining time)
	totalDuration := endTime - startTime       // 3600 seconds
	remainingDuration := endTime - currentTime // 2700 seconds

	// Set balances including both vested and unvested portions
	bankKeeper.SetBalance(genesisAddr, originalVestingCoins)

	// Execute vesting schedule transfer
	err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	require.NoError(t, err)

	// Verify recipient account has correct vesting schedule
	recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
	require.NotNil(t, recipientAccount)

	recipientVestingAcc, isVesting := recipientAccount.(*vestingtypes.ContinuousVestingAccount)
	require.True(t, isVesting)

	// Verify timeline preservation
	require.Equal(t, endTime, recipientVestingAcc.EndTime, "end time should be preserved")
	require.Equal(t, currentTime, recipientVestingAcc.StartTime, "start time should be current time")

	// Verify proportional amount calculation
	expectedRemainingAmount := originalVestingCoins[0].Amount.MulRaw(remainingDuration).QuoRaw(totalDuration)
	actualRemainingAmount := recipientVestingAcc.OriginalVesting[0].Amount

	// Allow for small rounding differences
	diff := expectedRemainingAmount.Sub(actualRemainingAmount).Abs()
	require.True(t, diff.LTE(math.NewInt(1)), "remaining amount should be proportionally correct")
}

// TestVestingIntegration_MultipleVestingAccountTypes tests handling multiple vesting account types in sequence
func TestVestingIntegration_MultipleVestingAccountTypes(t *testing.T) {
	ctx, k, accountKeeper, bankKeeper, _, _ := setupVestingIntegrationKeepers(t)

	currentTime := time.Now().Unix()
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Test different vesting account types
	testCases := []struct {
		name         string
		genesisAddr  string
		setupVesting func(addr sdk.AccAddress) sdk.AccountI
	}{
		{
			name:        "periodic_vesting",
			genesisAddr: testutil.Creator,
			setupVesting: func(addr sdk.AccAddress) sdk.AccountI {
				baseAccount := authtypes.NewBaseAccountWithAddress(addr)
				vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(600)))
				periods := []vestingtypes.Period{
					{Length: 1800, Amount: sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300)))},
					{Length: 1800, Amount: sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(300)))},
				}
				acc, _ := vestingtypes.NewPeriodicVestingAccount(baseAccount, vestingCoins, currentTime, periods)
				return acc
			},
		},
		{
			name:        "continuous_vesting",
			genesisAddr: testutil.Requester,
			setupVesting: func(addr sdk.AccAddress) sdk.AccountI {
				baseAccount := authtypes.NewBaseAccountWithAddress(addr)
				vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(800)))
				acc, _ := vestingtypes.NewContinuousVestingAccount(baseAccount, vestingCoins, currentTime, currentTime+3600)
				return acc
			},
		},
		{
			name:        "delayed_vesting",
			genesisAddr: testutil.Executor,
			setupVesting: func(addr sdk.AccAddress) sdk.AccountI {
				baseAccount := authtypes.NewBaseAccountWithAddress(addr)
				vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
				acc, _ := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, currentTime+7200)
				return acc
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Parse addresses
			genesisAddr, err := sdk.AccAddressFromBech32(tc.genesisAddr)
			require.NoError(t, err)
			recipientAddr := sdk.AccAddress("recipient_" + tc.name)

			// Set up vesting account
			vestingAccount := tc.setupVesting(genesisAddr)
			accountKeeper.SetAccount(ctx, vestingAccount)

			// Set balances to match the vesting account's original vesting amount
			var vestingCoins sdk.Coins
			if periodicAcc, ok := vestingAccount.(*vestingtypes.PeriodicVestingAccount); ok {
				vestingCoins = periodicAcc.OriginalVesting
			} else if continuousAcc, ok := vestingAccount.(*vestingtypes.ContinuousVestingAccount); ok {
				vestingCoins = continuousAcc.OriginalVesting
			} else if delayedAcc, ok := vestingAccount.(*vestingtypes.DelayedVestingAccount); ok {
				vestingCoins = delayedAcc.OriginalVesting
			} else {
				// Fallback for other vesting types
				vestingCoins = sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(500)))
			}
			bankKeeper.SetBalance(genesisAddr, vestingCoins)

			// Execute vesting transfer
			err = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
			require.NoError(t, err, "vesting transfer should succeed for %s", tc.name)

			// Verify recipient account was created
			recipientAccount := accountKeeper.GetAccount(ctx, recipientAddr)
			require.NotNil(t, recipientAccount, "recipient account should be created for %s", tc.name)

			// Verify recipient received the vesting coins
			recipientBalance := bankKeeper.GetAllBalances(ctx, recipientAddr)
			require.False(t, recipientBalance.IsZero(), "recipient should receive vesting coins for %s", tc.name)
		})
	}
}

// TestVestingIntegration_GetVestingInfoComprehensive tests GetVestingInfo with real vesting accounts
func TestVestingIntegration_GetVestingInfoComprehensive(t *testing.T) {
	ctx, k, accountKeeper, _, _, _ := setupVestingIntegrationKeepers(t)

	// Create test address
	testAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)

	currentTime := time.Now().Unix()
	ctx = ctx.WithBlockTime(time.Unix(currentTime, 0))

	// Test with non-vesting account
	t.Run("non_vesting_account", func(t *testing.T) {
		baseAccount := authtypes.NewBaseAccountWithAddress(testAddr)
		accountKeeper.SetAccount(ctx, baseAccount)

		isVesting, vestingCoins, endTime, err := k.GetVestingInfo(ctx, testAddr)
		require.NoError(t, err)
		require.False(t, isVesting)
		require.Nil(t, vestingCoins)
		require.Equal(t, int64(0), endTime)
	})

	// Test with active delayed vesting account
	t.Run("active_delayed_vesting", func(t *testing.T) {
		baseAccount := authtypes.NewBaseAccountWithAddress(testAddr)
		vestingCoins := sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000)))
		endTime := currentTime + 3600

		delayedVestingAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, endTime)
		require.NoError(t, err)

		accountKeeper.SetAccount(ctx, delayedVestingAcc)

		isVesting, returnedVestingCoins, returnedEndTime, err := k.GetVestingInfo(ctx, testAddr)
		require.NoError(t, err)
		require.True(t, isVesting)
		require.Equal(t, vestingCoins, returnedVestingCoins)
		require.Equal(t, endTime, returnedEndTime)
	})
}
