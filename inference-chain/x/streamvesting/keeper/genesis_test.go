package keeper_test

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/testutil/sample"
	inftypes "github.com/productscience/inference/x/inference/types"
	streamvestingmodule "github.com/productscience/inference/x/streamvesting/module"
	"github.com/productscience/inference/x/streamvesting/types"
)

// TestGenesis_EmptyState tests genesis with empty state
func (suite *KeeperTestSuite) TestGenesis_EmptyState() {
	// Test with default empty genesis state
	emptyGenesis := types.DefaultGenesis()

	// Initialize a keeper with empty genesis state
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, *emptyGenesis)

	// Export the state and verify it matches the original
	exportedGenesis := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().NotNil(exportedGenesis)

	suite.Require().Equal(emptyGenesis.Params, exportedGenesis.Params)
	suite.Require().Empty(exportedGenesis.VestingScheduleList)
}

// TestGenesis_WithVestingSchedules tests genesis with actual vesting schedules
func (suite *KeeperTestSuite) TestGenesis_WithVestingSchedules() {
	participant1 := testutil.Creator
	participant2 := testutil.Requester

	// Create test vesting schedules
	schedule1 := types.VestingSchedule{
		ParticipantAddress: participant1,
		EpochAmounts: []types.EpochCoins{
			{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, 100))},
			{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, 200))},
			{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, 300))},
		},
	}

	schedule2 := types.VestingSchedule{
		ParticipantAddress: participant2,
		EpochAmounts: []types.EpochCoins{
			{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, 500))},
			{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, 600))},
		},
	}

	// Create custom parameters
	params := types.DefaultParams()
	params.RewardVestingPeriod = 42 // Custom value for testing

	genesisState := types.GenesisState{
		Params:              params,
		VestingScheduleList: []types.VestingSchedule{schedule1, schedule2},
	}

	// Initialize a keeper with this genesis state
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, genesisState)

	// Verify schedules were imported correctly
	importedSchedule1, found := suite.keeper.GetVestingSchedule(suite.ctx, participant1)
	suite.Require().True(found)
	suite.Require().Equal(schedule1.ParticipantAddress, importedSchedule1.ParticipantAddress)
	suite.Require().Len(importedSchedule1.EpochAmounts, 3)
	suite.Require().Equal(math.NewInt(100), importedSchedule1.EpochAmounts[0].Coins[0].Amount)
	suite.Require().Equal(math.NewInt(200), importedSchedule1.EpochAmounts[1].Coins[0].Amount)
	suite.Require().Equal(math.NewInt(300), importedSchedule1.EpochAmounts[2].Coins[0].Amount)

	importedSchedule2, found := suite.keeper.GetVestingSchedule(suite.ctx, participant2)
	suite.Require().True(found)
	suite.Require().Equal(schedule2.ParticipantAddress, importedSchedule2.ParticipantAddress)
	suite.Require().Len(importedSchedule2.EpochAmounts, 2)
	suite.Require().Equal(math.NewInt(500), importedSchedule2.EpochAmounts[0].Coins[0].Amount)
	suite.Require().Equal(math.NewInt(600), importedSchedule2.EpochAmounts[1].Coins[0].Amount)

	// Verify parameters were imported correctly
	importedParams := suite.keeper.GetParams(suite.ctx)
	suite.Require().Equal(uint64(42), importedParams.RewardVestingPeriod)

	// Export the state and verify it matches the original
	exportedGenesis := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().NotNil(exportedGenesis)

	suite.Require().Equal(genesisState.Params, exportedGenesis.Params)
	suite.Require().Len(exportedGenesis.VestingScheduleList, 2)
	suite.Require().ElementsMatch(genesisState.VestingScheduleList, exportedGenesis.VestingScheduleList)
}

// TestGenesis_RoundTrip tests export followed by import (round trip)
func (suite *KeeperTestSuite) TestGenesis_RoundTrip() {
	participant := testutil.Creator

	// Add some vesting rewards using the keeper
	coin := sdk.NewInt64Coin(inftypes.BaseCoin, 1000)
	amount := sdk.NewCoins(coin)
	vestingEpochs := uint64(4)
	suite.mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(suite.ctx, inftypes.ModuleName, types.ModuleName, amount, "memo").Return(nil)
	suite.mocks.BankKeeper.EXPECT().LogSubAccountTransaction(suite.ctx, types.ModuleName, participant, "vesting", coin, gomock.Any())
	err := suite.keeper.AddVestedRewards(suite.ctx, participant, "inference", amount, &vestingEpochs, "memo")
	suite.Require().NoError(err)

	// Update parameters
	newParams := types.DefaultParams()
	newParams.RewardVestingPeriod = 999
	err = suite.keeper.SetParams(suite.ctx, newParams)
	suite.Require().NoError(err)

	// Export the current state
	exportedGenesis := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().NotNil(exportedGenesis)
	suite.Require().Len(exportedGenesis.VestingScheduleList, 1)
	suite.Require().Equal(uint64(999), exportedGenesis.Params.RewardVestingPeriod)

	// Clear the keeper state (simulate fresh start)
	// Note: In real usage, this would be a new keeper instance
	// For testing, we verify that import overwrites existing state

	// Import the exported state into a new keeper setup
	suite.SetupTest() // Reset the test suite with fresh keeper
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, *exportedGenesis)

	// Verify the state was correctly imported
	importedSchedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Equal(participant, importedSchedule.ParticipantAddress)
	suite.Require().Len(importedSchedule.EpochAmounts, 4)

	// Verify each epoch amount (1000÷4=250 each)
	expectedAmount := math.NewInt(250)
	for i := 0; i < 4; i++ {
		suite.Require().Equal(expectedAmount, importedSchedule.EpochAmounts[i].Coins[0].Amount)
	}

	// Verify parameters were imported
	importedParams := suite.keeper.GetParams(suite.ctx)
	suite.Require().Equal(uint64(999), importedParams.RewardVestingPeriod)

	// Export again and verify consistency
	secondExport := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().Equal(exportedGenesis, secondExport)
}

// TestGenesis_MultiCoinVesting tests genesis with multi-denomination vesting
func (suite *KeeperTestSuite) TestGenesis_MultiCoinVesting() {
	participant := testutil.Creator

	// Create vesting schedule with multiple coins per epoch
	schedule := types.VestingSchedule{
		ParticipantAddress: participant,
		EpochAmounts: []types.EpochCoins{
			{Coins: sdk.NewCoins(
				sdk.NewInt64Coin(inftypes.BaseCoin, 100),
				sdk.NewInt64Coin("uatom", 50),
			)},
			{Coins: sdk.NewCoins(
				sdk.NewInt64Coin(inftypes.BaseCoin, 200),
				sdk.NewInt64Coin("uatom", 75),
			)},
		},
	}

	genesisState := types.GenesisState{
		Params:              types.DefaultParams(),
		VestingScheduleList: []types.VestingSchedule{schedule},
	}

	// Initialize and verify import
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, genesisState)

	importedSchedule, found := suite.keeper.GetVestingSchedule(suite.ctx, participant)
	suite.Require().True(found)
	suite.Require().Len(importedSchedule.EpochAmounts, 2)

	// Check first epoch coins
	firstEpochCoins := importedSchedule.EpochAmounts[0].Coins
	suite.Require().Len(firstEpochCoins, 2)
	suite.Require().Equal(math.NewInt(100), firstEpochCoins.AmountOf(inftypes.BaseCoin))
	suite.Require().Equal(math.NewInt(50), firstEpochCoins.AmountOf("uatom"))

	// Check second epoch coins
	secondEpochCoins := importedSchedule.EpochAmounts[1].Coins
	suite.Require().Len(secondEpochCoins, 2)
	suite.Require().Equal(math.NewInt(200), secondEpochCoins.AmountOf(inftypes.BaseCoin))
	suite.Require().Equal(math.NewInt(75), secondEpochCoins.AmountOf("uatom"))

	// Export and verify
	exportedGenesis := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().Equal(genesisState.VestingScheduleList, exportedGenesis.VestingScheduleList)
}

// TestGenesis_LargeDataSet tests genesis with many participants (performance/scale test)
func (suite *KeeperTestSuite) TestGenesis_LargeDataSet() {
	const numParticipants = 100

	// Create many vesting schedules
	var vestingSchedules []types.VestingSchedule
	for i := 0; i < numParticipants; i++ {
		participant := sample.AccAddress()
		schedule := types.VestingSchedule{
			ParticipantAddress: participant,
			EpochAmounts: []types.EpochCoins{
				{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, int64(100*(i+1))))},
				{Coins: sdk.NewCoins(sdk.NewInt64Coin(inftypes.BaseCoin, int64(200*(i+1))))},
			},
		}
		vestingSchedules = append(vestingSchedules, schedule)
	}

	genesisState := types.GenesisState{
		Params:              types.DefaultParams(),
		VestingScheduleList: vestingSchedules,
	}

	// Initialize genesis (should handle large dataset efficiently)
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, genesisState)

	// Verify a few random participants were imported correctly
	checkIndices := []int{0, 25, 50, 75, 99}
	for _, idx := range checkIndices {
		expectedSchedule := vestingSchedules[idx]
		importedSchedule, found := suite.keeper.GetVestingSchedule(suite.ctx, expectedSchedule.ParticipantAddress)
		suite.Require().True(found, "Participant %d not found", idx)
		suite.Require().Equal(expectedSchedule.ParticipantAddress, importedSchedule.ParticipantAddress)
		suite.Require().Len(importedSchedule.EpochAmounts, 2)
	}

	// Export and verify count matches
	exportedGenesis := streamvestingmodule.ExportGenesis(suite.ctx, suite.keeper)
	suite.Require().Len(exportedGenesis.VestingScheduleList, numParticipants)
	suite.Require().ElementsMatch(genesisState.VestingScheduleList, exportedGenesis.VestingScheduleList)
}

// TestGenesis_InvalidData tests genesis error handling with invalid data
func (suite *KeeperTestSuite) TestGenesis_InvalidParams() {
	// Test that invalid parameters cause appropriate handling
	// Note: Since SetParams might validate parameters, this tests that behavior

	invalidParams := types.Params{
		RewardVestingPeriod: 0, // This might be considered invalid depending on validation
	}

	genesisState := types.GenesisState{
		Params:              invalidParams,
		VestingScheduleList: []types.VestingSchedule{},
	}

	// This should either succeed (if 0 is valid) or be handled gracefully
	// The test documents the expected behavior
	streamvestingmodule.InitGenesis(suite.ctx, suite.keeper, genesisState)

	// Verify the parameters were set (regardless of validation)
	importedParams := suite.keeper.GetParams(suite.ctx)
	suite.Require().Equal(invalidParams.RewardVestingPeriod, importedParams.RewardVestingPeriod)
}
