package keeper_test

import (
	"testing"

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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 1000))
	vestingEpochs := uint64(5)

	// Add the first reward
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
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
		suite.Require().Equal("nicoin", epochAmount.Coins[0].Denom)
	}
}

// Test AddVestedRewards with remainder handling
func (suite *KeeperTestSuite) TestAddVestedRewards_WithRemainder() {
	participant := testutil.Creator
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 1003)) // 1003 / 4 = 250 remainder 3
	vestingEpochs := uint64(4)

	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
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
	amount1 := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 900))
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount1, &vestingEpochs)
	suite.Require().NoError(err)

	// Add second reward of 600 coins (200 per epoch)
	amount2 := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 600))
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, amount2, &vestingEpochs)
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
	amount1 := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 600))
	vestingEpochs1 := uint64(2)
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount1, &vestingEpochs1)
	suite.Require().NoError(err)

	// Add second reward with 4 epochs (should extend array)
	amount2 := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 800))
	vestingEpochs2 := uint64(4)
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, amount2, &vestingEpochs2)
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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 1800))

	// Don't specify vesting epochs (should use default parameter)
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, nil)
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
	aliceAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 500))
	bobAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 300))
	vestingEpochs := uint64(3)

	err := suite.keeper.AddVestedRewards(suite.ctx, alice, aliceAmount, &vestingEpochs)
	suite.Require().NoError(err)
	err = suite.keeper.AddVestedRewards(suite.ctx, bob, bobAmount, &vestingEpochs)
	suite.Require().NoError(err)

	// Mock bank keeper to expect transfers
	aliceAddr, _ := sdk.AccAddressFromBech32(alice)
	bobAddr, _ := sdk.AccAddressFromBech32(bob)

	aliceUnlockAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 168)) // 500/3 with remainder in first (166+2)
	bobUnlockAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 100))   // 300/3

	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, aliceAddr, aliceUnlockAmount,
	).Return(nil)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, bobAddr, bobUnlockAmount,
	).Return(nil)

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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 300))
	vestingEpochs := uint64(2)

	// Add vesting schedule
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
	suite.Require().NoError(err)

	// Check initial schedule
	schedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(schedule.EpochAmounts, 2)

	// Mock bank keeper
	addr, _ := sdk.AccAddressFromBech32(participant)
	unlockAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 150)) // 300/2
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, unlockAmount,
	).Return(nil)

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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 100))
	vestingEpochs := uint64(1) // Only one epoch

	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
	suite.Require().NoError(err)

	// Mock bank keeper
	addr, _ := sdk.AccAddressFromBech32(participant)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, amount,
	).Return(nil)

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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 300))
	vestingEpochs := uint64(2)

	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
	suite.Require().NoError(err)

	// Mock bank keeper for the unlock
	addr, _ := sdk.AccAddressFromBech32(participant)
	unlockAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 150)) // 300/2
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		suite.ctx, types.ModuleName, addr, unlockAmount,
	).Return(nil)

	// Call AdvanceEpoch
	completedEpoch := uint64(100)
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
	amount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 100))
	vestingEpochs := uint64(0)
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
	suite.Require().Error(err)
	suite.Require().Contains(err.Error(), "vesting epochs cannot be zero")

	// Test with empty amount - should succeed (no-op)
	emptyAmount := sdk.NewCoins()
	vestingEpochs = uint64(5)
	err = suite.keeper.AddVestedRewards(suite.ctx, participant, emptyAmount, &vestingEpochs)
	suite.Require().NoError(err) // Should not error, just do nothing

	// Test with invalid participant address
	invalidParticipant := "invalid-address"
	validAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 100))
	err = suite.keeper.AddVestedRewards(suite.ctx, invalidParticipant, validAmount, &vestingEpochs)
	suite.Require().Error(err)
	suite.Require().Contains(err.Error(), "invalid participant address")
}

// Test GetAllVestingSchedules
func (suite *KeeperTestSuite) TestGetAllVestingSchedules() {
	alice := testutil.Creator
	bob := testutil.Requester

	// Add schedules for both participants
	aliceAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 400))
	bobAmount := sdk.NewCoins(sdk.NewInt64Coin("nicoin", 600))
	vestingEpochs := uint64(2)

	err := suite.keeper.AddVestedRewards(suite.ctx, alice, aliceAmount, &vestingEpochs)
	suite.Require().NoError(err)
	err = suite.keeper.AddVestedRewards(suite.ctx, bob, bobAmount, &vestingEpochs)
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
		sdk.NewInt64Coin("nicoin", 600),
		sdk.NewInt64Coin("stake", 300),
	)
	vestingEpochs := uint64(3)

	err := suite.keeper.AddVestedRewards(suite.ctx, participant, amount, &vestingEpochs)
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
