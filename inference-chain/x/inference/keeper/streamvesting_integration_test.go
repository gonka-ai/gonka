package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/log"
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
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	streamvestingkeeper "github.com/productscience/inference/x/streamvesting/keeper"
	streamvestingtypes "github.com/productscience/inference/x/streamvesting/types"
)

// Test streamvesting integration with inference module

func setupKeeperWithMocksForStreamVesting(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	return k, keeper.NewMsgServerImpl(k), ctx, &mocks
}

func setupRealStreamVestingKeepers(t testing.TB) (sdk.Context, keeper.Keeper, streamvestingkeeper.Keeper, types.MsgServer, streamvestingtypes.MsgServer) {
	// --- Store and Codec Setup ---
	inferenceStoreKey := storetypes.NewKVStoreKey(types.StoreKey)
	streamvestingStoreKey := storetypes.NewKVStoreKey(streamvestingtypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(inferenceStoreKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(streamvestingStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	// --- Mock Keepers ---
	ctrl := gomock.NewController(t)
	bookkeepingBankKeeper := keepertest.NewMockBookkeepingBankKeeper(ctrl)
	bankViewKeeper := keepertest.NewMockBankKeeper(ctrl)
	accountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	validatorSet := keepertest.NewMockValidatorSet(ctrl)
	groupMock := keepertest.NewMockGroupMessageKeeper(ctrl)
	stakingKeeper := keepertest.NewMockStakingKeeper(ctrl)
	collateralKeeper := keepertest.NewMockCollateralKeeper(ctrl)
	authzKeeper := keepertest.NewMockAuthzKeeper(ctrl)

	// --- Real Keepers ---
	svKeeper := streamvestingkeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(streamvestingStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
		nil,                   // bank keeper
		bookkeepingBankKeeper, // bank escrow keeper
	)

	// Create a BLS keeper for testing (similar to testutil/keeper/inference.go)
	blsStoreKey := storetypes.NewKVStoreKey("bls")
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
	)

	upgradeMock := keepertest.NewMockUpgradeKeeper(ctrl)
	inferenceKeeper := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(inferenceStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
		bookkeepingBankKeeper,
		bankViewKeeper,
		groupMock,
		validatorSet,
		stakingKeeper,
		accountKeeper,
		blsKeeper,
		collateralKeeper,
		svKeeper,
		authzKeeper,
		nil,
		upgradeMock,
	)

	// Initialize default params for both keepers
	require.NoError(t, svKeeper.SetParams(ctx, streamvestingtypes.DefaultParams()))
	require.NoError(t, inferenceKeeper.SetParams(ctx, types.DefaultParams()))

	inferenceMsgSrv := keeper.NewMsgServerImpl(inferenceKeeper)
	streamvestingMsgSrv := streamvestingkeeper.NewMsgServerImpl(svKeeper)

	// Mock necessary bank calls
	bookkeepingBankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkeepingBankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkeepingBankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkeepingBankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	return ctx, inferenceKeeper, svKeeper, inferenceMsgSrv, streamvestingMsgSrv
}

func TestVestingIntegration_ParameterBased(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForStreamVesting(t)

	// Set parameters for vesting periods
	params := types.DefaultParams()
	params.TokenomicsParams.WorkVestingPeriod = 5      // 5 epochs for work coins
	params.TokenomicsParams.RewardVestingPeriod = 10   // 10 epochs for reward coins
	params.TokenomicsParams.TopMinerVestingPeriod = 15 // 15 epochs for top miner rewards
	k.SetParams(ctx, params)

	participantAddrStr := sample.AccAddress()

	// Test case 1: Work coins should use vesting when WorkVestingPeriod > 0
	workAmount := int64(1000)
	workVestingPeriod := uint64(5)

	// Mock expectations for vesting flow (escrow payment goes through inference module)
	expectedWorkCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(workAmount)))
	mocks.StreamVestingKeeper.EXPECT().
		AddVestedRewards(ctx, participantAddrStr, "inference", expectedWorkCoins, &workVestingPeriod, gomock.Any()).
		Return(nil)

	// Execute payment from escrow
	err := k.PayParticipantFromEscrow(ctx, participantAddrStr, workAmount, "test-memo", &workVestingPeriod)
	require.NoError(t, err)

	// Test case 2: Reward coins should use vesting when RewardVestingPeriod > 0
	rewardAmount := int64(2000)
	rewardVestingPeriod := uint64(10)

	expectedRewardCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(rewardAmount)))
	mocks.StreamVestingKeeper.EXPECT().
		AddVestedRewards(ctx, participantAddrStr, "inference", expectedRewardCoins, &rewardVestingPeriod, gomock.Any()).
		Return(nil)

	// Execute payment from top reward pool module
	err = k.PayParticipantFromModule(ctx, participantAddrStr, rewardAmount, types.TopRewardPoolAccName, "reward-memo", &rewardVestingPeriod)
	require.NoError(t, err)
}

func TestVestingIntegration_DirectPayment(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForStreamVesting(t)

	// Set parameters for zero vesting periods (direct payment)
	params := types.DefaultParams()
	params.TokenomicsParams.WorkVestingPeriod = 0
	params.TokenomicsParams.RewardVestingPeriod = 0
	k.SetParams(ctx, params)

	participantAddrStr := sample.AccAddress()
	participantAddr, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)

	// Test: Zero vesting period should result in direct payment
	amount := int64(1000)
	zeroVestingPeriod := uint64(0)

	expectedCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(amount)))

	// Mock expectation for direct payment (no vesting)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(ctx, types.TopRewardPoolAccName, participantAddr, expectedCoins, gomock.Any()).
		Return(nil)

	// No vesting keeper calls should be made

	// Execute payment with zero vesting period
	err = k.PayParticipantFromModule(ctx, participantAddrStr, amount, types.TopRewardPoolAccName, "direct-payment", &zeroVestingPeriod)
	require.NoError(t, err)
}

func TestVestingIntegration_EpochAdvancement(t *testing.T) {
	ctx, _, svKeeper, _, _ := setupRealStreamVestingKeepers(t)

	participantAddrStr := sample.AccAddress()

	// Step 1: Add vested rewards for the participant (3-epoch vesting)
	vestingAmount := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000))
	vestingEpochs := uint64(3)

	err := svKeeper.AddVestedRewards(ctx, participantAddrStr, "inference", vestingAmount, &vestingEpochs, "")
	require.NoError(t, err)

	// Verify initial vesting schedule was created
	schedule, found := svKeeper.GetVestingSchedule(ctx, participantAddrStr)
	require.True(t, found)
	require.Len(t, schedule.EpochAmounts, 3) // Should have 3 epochs

	// Each epoch should have ~333 coins (1000÷3=333 + remainder in first epoch)
	expectedPerEpoch := int64(333)
	expectedFirstEpoch := int64(334) // Gets the remainder (1000 - 333*3 = 1)

	require.Equal(t, expectedFirstEpoch, schedule.EpochAmounts[0].Coins[0].Amount.Int64())
	require.Equal(t, expectedPerEpoch, schedule.EpochAmounts[1].Coins[0].Amount.Int64())
	require.Equal(t, expectedPerEpoch, schedule.EpochAmounts[2].Coins[0].Amount.Int64())

	// Step 2: Process first epoch unlock
	err = svKeeper.AdvanceEpoch(ctx, 1)
	require.NoError(t, err)

	// Verify schedule was updated - first epoch should be removed
	schedule, found = svKeeper.GetVestingSchedule(ctx, participantAddrStr)
	require.True(t, found)
	require.Len(t, schedule.EpochAmounts, 2) // Should now have 2 epochs left

	// Remaining epochs should be unchanged
	require.Equal(t, expectedPerEpoch, schedule.EpochAmounts[0].Coins[0].Amount.Int64())
	require.Equal(t, expectedPerEpoch, schedule.EpochAmounts[1].Coins[0].Amount.Int64())

	// Step 3: Process second epoch unlock
	err = svKeeper.AdvanceEpoch(ctx, 2)
	require.NoError(t, err)

	// Verify schedule has 1 epoch left
	schedule, found = svKeeper.GetVestingSchedule(ctx, participantAddrStr)
	require.True(t, found)
	require.Len(t, schedule.EpochAmounts, 1)
	require.Equal(t, expectedPerEpoch, schedule.EpochAmounts[0].Coins[0].Amount.Int64())

	// Step 4: Process final epoch unlock
	err = svKeeper.AdvanceEpoch(ctx, 3)
	require.NoError(t, err)

	// Verify schedule is completely removed (empty schedule cleanup)
	_, found = svKeeper.GetVestingSchedule(ctx, participantAddrStr)
	require.False(t, found) // Schedule should be deleted when empty
}

func TestVestingIntegration_MixedVestingScenario(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForStreamVesting(t)

	// Setup mixed vesting periods
	params := types.DefaultParams()
	params.TokenomicsParams.WorkVestingPeriod = 0   // Direct payment for work
	params.TokenomicsParams.RewardVestingPeriod = 7 // Vested over 7 epochs for rewards
	k.SetParams(ctx, params)

	participantAddrStr := sample.AccAddress()
	participantAddr, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)

	// Test case 1: Work payment should be direct (vesting period = 0)
	workAmount := int64(500)
	workVestingPeriod := uint64(0)

	expectedWorkCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(workAmount)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddr, expectedWorkCoins, gomock.Any()).
		Return(nil)

	err = k.PayParticipantFromEscrow(ctx, participantAddrStr, workAmount, "work-payment", &workVestingPeriod)
	require.NoError(t, err)

	// Test case 2: Reward payment should be vested (vesting period = 7)
	rewardAmount := int64(1500)
	rewardVestingPeriod := uint64(7)

	expectedRewardCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(rewardAmount)))
	mocks.StreamVestingKeeper.EXPECT().
		AddVestedRewards(ctx, participantAddrStr, "inference", expectedRewardCoins, &rewardVestingPeriod, gomock.Any()).
		Return(nil)

	err = k.PayParticipantFromModule(ctx, participantAddrStr, rewardAmount, types.TopRewardPoolAccName, "reward-payment", &rewardVestingPeriod)
	require.NoError(t, err)
}

func TestVestingIntegration_TopMinerRewards(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForStreamVesting(t)

	// Configure top miner vesting period
	params := types.DefaultParams()
	params.TokenomicsParams.TopMinerVestingPeriod = 15 // 15 epochs for top miner rewards
	k.SetParams(ctx, params)

	participantAddrStr := sample.AccAddress()
	rewardAmount := int64(5000)
	topMinerVestingPeriod := uint64(15)

	expectedCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(rewardAmount)))

	mocks.StreamVestingKeeper.EXPECT().
		AddVestedRewards(ctx, participantAddrStr, "inference", expectedCoins, &topMinerVestingPeriod, gomock.Any()).
		Return(nil)

	// Execute top miner reward payment
	err := k.PayParticipantFromModule(ctx, participantAddrStr, rewardAmount, types.TopRewardPoolAccName, "top-miner-reward", &topMinerVestingPeriod)
	require.NoError(t, err)
}

func TestVestingIntegration_ParameterValidation(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForStreamVesting(t)

	// Test that module accepts valid vesting period parameters
	params := types.DefaultParams()
	params.TokenomicsParams.WorkVestingPeriod = 0
	params.TokenomicsParams.RewardVestingPeriod = 180
	params.TokenomicsParams.TopMinerVestingPeriod = 365

	// Should not error on valid parameters
	err := k.SetParams(ctx, params)
	require.NoError(t, err)

	// Retrieve and verify parameters
	retrievedParams := k.GetParams(ctx)
	require.Equal(t, uint64(0), retrievedParams.TokenomicsParams.WorkVestingPeriod)
	require.Equal(t, uint64(180), retrievedParams.TokenomicsParams.RewardVestingPeriod)
	require.Equal(t, uint64(365), retrievedParams.TokenomicsParams.TopMinerVestingPeriod)
}

func TestVestingIntegration_ErrorHandling(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForStreamVesting(t)

	participantAddrStr := sample.AccAddress()
	amount := int64(1000)
	vestingPeriod := uint64(5)

	expectedCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, int64(amount)))

	// Test case 2: Vesting keeper failure should be handled
	mocks.StreamVestingKeeper.EXPECT().
		AddVestedRewards(ctx, participantAddrStr, types.ModuleName, expectedCoins, &vestingPeriod, gomock.Any()).
		Return(fmt.Errorf("invalid request"))

	err := k.PayParticipantFromModule(ctx, participantAddrStr, amount, types.TopRewardPoolAccName, "vesting-error-test", &vestingPeriod)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid request")
}
