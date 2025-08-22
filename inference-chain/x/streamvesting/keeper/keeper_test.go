package keeper_test

import (
	"testing"

	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/streamvesting/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
	"github.com/stretchr/testify/suite"
)

type KeeperTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper keeper.Keeper
	mocks  keepertest.StreamVestingMocks
}

func (suite *KeeperTestSuite) SetupTest() {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(suite.T())
	suite.ctx = ctx
	suite.keeper = k
	suite.mocks = mocks
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}

// Test AddVestedRewards with a single reward
func (suite *KeeperTestSuite) TestAddVestedRewards_SingleReward() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1000))
	vestingEpochs := uint64(5)

	// Add the first reward
	coin := sdk.NewInt64Coin("ngonka", 1000)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Check that the schedule was created correctly
	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Equal(participant, schedule.ParticipantAddress)
	suite.Require().Len(schedule.EpochAmounts, 5)

	// Each epoch should have 200 coins (1000 / 5)
	expectedPerEpoch := math.NewInt(200)
	for _, epochAmount := range schedule.EpochAmounts {
		suite.Require().Len(epochAmount.Coins, 1)
		suite.Require().Equal(expectedPerEpoch, epochAmount.Coins[0].Amount)
		suite.Require().Equal("ngonka", epochAmount.Coins[0].Denom)
	}
}

// Test AddVestedRewards with remainder handling
func (suite *KeeperTestSuite) TestAddVestedRewards_WithRemainder() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1003)) // 1003 / 4 = 250 remainder 3
	vestingEpochs := uint64(4)

	coin := sdk.NewInt64Coin("ngonka", 1003)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 4)

	// First epoch should have 250 + 3 (remainder) = 253
	suite.Require().Len(schedule.EpochAmounts[0].Coins, 1)
	suite.Require().Equal(math.NewInt(253), schedule.EpochAmounts[0].Coins[0].Amount)
	// Other epochs should have 250 each
	for i := 1; i < 4; i++ {
		suite.Require().Len(schedule.EpochAmounts[i].Coins, 1)
		suite.Require().Equal(math.NewInt(250), schedule.EpochAmounts[i].Coins[0].Amount)
	}
}

// Test AddVestedRewards with aggregation (adding to existing schedule)
func (suite *KeeperTestSuite) TestAddVestedRewards_Aggregation() {
	participant := testutil.Creator
	vestingEpochs := uint64(3)

	// Add first reward of 900 coins (300 per epoch)
	coin := sdk.NewInt64Coin("ngonka", 900)
	amount1 := sdk.NewCoins(coin)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount1, "memo")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount1, &vestingEpochs, "memo")
	suite.Require().NoError(err)

	// Add second reward of 600 coins (200 per epoch)
	amount2 := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 600))
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount2, "mem")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", sdk.NewInt64Coin("ngonka", 600), gomock.Any())
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount2, &vestingEpochs, "mem")
	suite.Require().NoError(err)

	// Check that amounts were aggregated correctly
	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 3)

	// Each epoch should have 300 + 200 = 500 coins
	expectedPerEpoch := math.NewInt(500)
	for _, epochAmount := range schedule.EpochAmounts {
		suite.Require().Len(epochAmount.Coins, 1)
		suite.Require().Equal(expectedPerEpoch, epochAmount.Coins[0].Amount)
	}
}

// Test AddVestedRewards with array extension
func (suite *KeeperTestSuite) TestAddVestedRewards_ArrayExtension() {
	participant := testutil.Creator

	// Add first reward with 2 epochs
	amount1 := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 600))
	vestingEpochs1 := uint64(2)
	coin1 := sdk.NewInt64Coin("ngonka", 600)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount1, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin1, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount1, &vestingEpochs1, "")
	suite.Require().NoError(err)

	// Add second reward with 4 epochs (should extend array)
	amount2 := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 800))
	vestingEpochs2 := uint64(4)
	coin2 := sdk.NewInt64Coin("ngonka", 800)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount2, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin2, gomock.Any())
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount2, &vestingEpochs2, "")
	suite.Require().NoError(err)

	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 4) // Extended to 4 epochs

	// First 2 epochs should have original amounts + new amounts
	suite.Require().Len(schedule.EpochAmounts[0].Coins, 1)
	suite.Require().Equal(math.NewInt(500), schedule.EpochAmounts[0].Coins[0].Amount) // 300 + 200
	suite.Require().Len(schedule.EpochAmounts[1].Coins, 1)
	suite.Require().Equal(math.NewInt(500), schedule.EpochAmounts[1].Coins[0].Amount) // 300 + 200
	// Last 2 epochs should have only new amounts
	suite.Require().Len(schedule.EpochAmounts[2].Coins, 1)
	suite.Require().Equal(math.NewInt(200), schedule.EpochAmounts[2].Coins[0].Amount)
	suite.Require().Len(schedule.EpochAmounts[3].Coins, 1)
	suite.Require().Equal(math.NewInt(200), schedule.EpochAmounts[3].Coins[0].Amount)
}

// Test AddVestedRewards using default vesting period parameter
func (suite *KeeperTestSuite) TestAddVestedRewards_DefaultVestingPeriod() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1800))

	// Don't specify vesting epochs (should use default parameter)
	coin := sdk.NewInt64Coin("ngonka", 1800)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, nil, "")
	suite.Require().NoError(err)

	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)

	// Should use default parameter (180 epochs)
	params := suite.keeper.GetParams(suite.ctx)
	expectedEpochs := int(params.RewardVestingPeriod)
	suite.Require().Len(schedule.EpochAmounts, expectedEpochs)

	// Each epoch should have 10 coins (1800 / 180)
	expectedPerEpoch := math.NewInt(10)
	for _, epochAmount := range schedule.EpochAmounts {
		suite.Require().Len(epochAmount.Coins, 1)
		suite.Require().Equal(expectedPerEpoch, epochAmount.Coins[0].Amount)
	}
}

// Test ProcessEpochUnlocks with multiple participants
func (suite *KeeperTestSuite) TestProcessEpochUnlocks_MultipleParticipants() {
	alice := testutil.Creator
	bob := testutil.Requester

	// Setup vesting schedules for both participants
	aliceAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 500))
	bobAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 300))
	vestingEpochs := uint64(3)

	aliceCoin := sdk.NewInt64Coin("ngonka", 500)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, aliceAmount, gomock.Any())
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, alice, "vesting", aliceCoin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, alice, "inference", aliceAmount, &vestingEpochs, "")
	suite.Require().NoError(err)

	bobCoin := sdk.NewInt64Coin("ngonka", 300)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, bobAmount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, bob, "vesting", bobCoin, gomock.Any())
	err = suite.keeper.AddVestedRewards(suite.ctx, bob, "inference", bobAmount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Mock bank keeper to expect transfers
	aliceAddr, _ := sdk.AccAddressFromBech32(alice)
	bobAddr, _ := sdk.AccAddressFromBech32(bob)

	aliceUnlockAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 168)) // 500/3 with remainder in first (166+2)
	bobUnlockAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 100))   // 300/3

	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, aliceAddr, aliceUnlockAmount, "vesting payment",
	).Return(nil)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, alice, types.ModuleName, "vesting", sdk.NewInt64Coin("ngonka", 168), gomock.Any())
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, bobAddr, bobUnlockAmount, gomock.Any(),
	).Return(nil)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, bob, types.ModuleName, "vesting", sdk.NewInt64Coin("ngonka", 100), gomock.Any())

	// Process epoch unlocks
	err = suite.keeper.ProcessEpochUnlocks(suite.ctx)
	suite.Require().NoError(err)

	// Check that schedules were updated correctly
	aliceSchedule, found := suite.keeper.GetVestingSchedule(suite.ctx, alice)
	suite.Require().True(found)
	suite.Require().Len(aliceSchedule.EpochAmounts, 2) // One epoch processed

	bobSchedule, found := suite.keeper.GetVestingSchedule(suite.ctx, bob)
	suite.Require().True(found)
	suite.Require().Len(bobSchedule.EpochAmounts, 2) // One epoch processed
}

// Debug test for epoch processing
func (suite *KeeperTestSuite) TestProcessEpochUnlocks_Debug() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 300))
	vestingEpochs := uint64(2)

	// Add vesting schedule
	coin := sdk.NewInt64Coin("ngonka", 300)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Check initial schedule
	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 2)

	// Mock bank keeper
	addr, _ := sdk.AccAddressFromBech32(participant)
	unlockAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 150)) // 300/2
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, unlockAmount, "vesting payment",
	).Return(nil)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, participant, types.ModuleName, "vesting", sdk.NewInt64Coin("ngonka", 150), gomock.Any())

	// Process unlocks
	err = suite.keeper.ProcessEpochUnlocks(suite.ctx)
	suite.Require().NoError(err)

	// Check updated schedule
	scheduleAfter, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(scheduleAfter.EpochAmounts, 1, "Should have 1 epoch remaining after processing")
}

// Test ProcessEpochUnlocks with empty schedule cleanup
func (suite *KeeperTestSuite) TestProcessEpochUnlocks_EmptyScheduleCleanup() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 100))
	vestingEpochs := uint64(1) // Only one epoch

	coin := sdk.NewInt64Coin("ngonka", 100)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Mock bank keeper
	addr, _ := sdk.AccAddressFromBech32(participant)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, amount, "vesting payment",
	).Return(nil)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, participant, types.ModuleName, "vesting", coin, gomock.Any())

	// Process the only epoch
	err = suite.keeper.ProcessEpochUnlocks(suite.ctx)
	suite.Require().NoError(err)

	// Schedule should be completely removed
	_, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().False(found)
}

// Test ProcessEpochUnlocks with no schedules (should not error)
func (suite *KeeperTestSuite) TestProcessEpochUnlocks_NoSchedules() {
	// Process unlocks when no schedules exist
	err := suite.keeper.ProcessEpochUnlocks(suite.ctx)
	suite.Require().NoError(err) // Should not error
}

// Test AdvanceEpoch function
func (suite *KeeperTestSuite) TestAdvanceEpoch() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 300))
	vestingEpochs := uint64(2)

	coin := sdk.NewInt64Coin("ngonka", 300)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, gomock.Any())
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Mock bank keeper for the unlock
	addr, _ := sdk.AccAddressFromBech32(participant)
	paidCoin := sdk.NewInt64Coin("ngonka", 150)
	unlockAmount := sdk.NewCoins(paidCoin) // 300/2
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, unlockAmount, "vesting payment",
	).Return(nil)

	// Call AdvanceEpoch
	completedEpoch := uint64(100)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, participant, types.ModuleName, "vesting", paidCoin, gomock.Any())
	err = suite.keeper.AdvanceEpoch(suite.ctx, completedEpoch)
	suite.Require().NoError(err)

	// Verify schedule was updated
	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 1) // One epoch unlocked
}

// Test error handling in AddVestedRewards
func (suite *KeeperTestSuite) TestAddVestedRewards_InvalidInputs() {
	participant := testutil.Creator

	// Test with zero vesting epochs
	amount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 100))
	vestingEpochs := uint64(0)
	coin := sdk.NewInt64Coin("ngonka", 100)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "").AnyTimes()
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any()).AnyTimes()
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().Error(err)
	suite.Require().Contains(err.Error(), "vesting epochs cannot be zero")

	// Test with empty amount - should succeed (no-op)
	emptyAmount := sdk.NewCoins()
	vestingEpochs = uint64(5)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, emptyAmount, "").AnyTimes()
	// No LogSubAccountTransaction mock needed for empty amount
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", emptyAmount, &vestingEpochs, "")
	suite.Require().NoError(err) // Should not error, just do nothing

	// Test with invalid participant address
	invalidParticipant := "invalid-address"
	validAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 100))
	validCoin := sdk.NewInt64Coin("ngonka", 100)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, validAmount, "").AnyTimes()
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, invalidParticipant, "vesting", validCoin, gomock.Any()).AnyTimes()
	err = suite.keeper.AddVestedRewards(suite.ctx, invalidParticipant, "inference", validAmount, &vestingEpochs, "")
	suite.Require().Error(err)
	suite.Require().Contains(err.Error(), "invalid participant address")
}

// Test GetAllVestingSchedules
func (suite *KeeperTestSuite) TestGetAllVestingSchedules() {
	alice := testutil.Creator
	bob := testutil.Requester

	// Add schedules for both participants
	aliceAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 400))
	bobAmount := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 600))
	vestingEpochs := uint64(2)

	aliceCoin := sdk.NewInt64Coin("ngonka", 400)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, aliceAmount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, alice, "vesting", aliceCoin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, alice, "inference", aliceAmount, &vestingEpochs, "")
	suite.Require().NoError(err)

	bobCoin := sdk.NewInt64Coin("ngonka", 600)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, bobAmount, "")
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, bob, "vesting", bobCoin, gomock.Any())
	err = suite.keeper.AddVestedRewards(suite.ctx, bob, "inference", bobAmount, &vestingEpochs, "")
	suite.Require().NoError(err)

	// Get all schedules
	schedules := suite.keeper.GetAllVestingSchedules(suite.ctx)
	suite.Require().Len(schedules, 2)

	// Verify both participants are present
	participantSet := make(map[string]bool)
	for _, schedule := range schedules {
		participantSet[schedule.ParticipantAddress] = true
	}
	suite.Require().True(participantSet[alice])
	suite.Require().True(participantSet[bob])
}

// Test multi-coin vesting (if supported)
func (suite *KeeperTestSuite) TestAddVestedRewards_MultiCoin() {
	participant := testutil.Creator
	amount := sdk.NewCoins(
		sdk.NewInt64Coin("ngonka", 600),
		sdk.NewInt64Coin("stake", 300),
	)
	vestingEpochs := uint64(3)

	// For multi-coin, we need to mock for each coin
	nicoin := sdk.NewInt64Coin("ngonka", 600)
	stake := sdk.NewInt64Coin("stake", 300)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, amount, "")
	// We need to mock for each coin in the amount
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", nicoin, gomock.Any())
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", stake, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "")
	suite.Require().NoError(err)

	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 3)

	// Each epoch should have both coins
	for _, epochAmount := range schedule.EpochAmounts {
		suite.Require().True(len(epochAmount.Coins) > 0)
		for _, coin := range epochAmount.Coins {
			suite.Require().True(coin.Amount.GT(math.ZeroInt()))
		}
		// Note: The specific amounts depend on how multi-coin is handled in implementation
	}
}
