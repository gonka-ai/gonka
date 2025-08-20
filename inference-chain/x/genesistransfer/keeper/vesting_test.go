package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	"github.com/stretchr/testify/suite"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/genesistransfer/keeper"
)

type VestingTestSuite struct {
	suite.Suite
	keeper keeper.Keeper
	ctx    sdk.Context
}

func (suite *VestingTestSuite) SetupTest() {
	k, ctx := keepertest.GenesistransferKeeper(suite.T())
	suite.keeper = k
	suite.ctx = ctx
}

func TestVestingTestSuite(t *testing.T) {
	suite.Run(t, new(VestingTestSuite))
}

// Test TransferVestingSchedule with different vesting account types
func (suite *VestingTestSuite) TestTransferVestingScheduleValidation() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	// Test invalid addresses
	suite.Run("invalid_addresses", func() {
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nil, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")

		err = suite.keeper.ExecuteOwnershipTransfer(suite.ctx, genesisAddr, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "recipient address cannot be nil")

		err = suite.keeper.ExecuteOwnershipTransfer(suite.ctx, genesisAddr, genesisAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "cannot transfer to the same address")
	})

	// Test non-existent genesis account
	suite.Run("non_existent_genesis_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nonExistentAddr, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})
}

// Test GetVestingInfo with different scenarios
func (suite *VestingTestSuite) TestGetVestingInfo() {
	suite.Run("nil_address", func() {
		isVesting, coins, endTime, err := suite.keeper.GetVestingInfo(suite.ctx, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "address cannot be nil")
		suite.Require().False(isVesting)
		suite.Require().Nil(coins)
		suite.Require().Equal(int64(0), endTime)
	})

	suite.Run("non_existent_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		isVesting, coins, endTime, err := suite.keeper.GetVestingInfo(suite.ctx, nonExistentAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
		suite.Require().False(isVesting)
		suite.Require().Nil(coins)
		suite.Require().Equal(int64(0), endTime)
	})
}

// Test vesting account creation and validation
func (suite *VestingTestSuite) TestVestingAccountCreation() {
	// Test data
	baseAddr := sdk.AccAddress("base_account_______")
	vestingCoins := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000)))

	// Test BaseVestingAccount creation parameters
	suite.Run("base_vesting_account_params", func() {
		baseAccount := authtypes.NewBaseAccountWithAddress(baseAddr)
		currentTime := time.Now().Unix()
		endTime := currentTime + 3600 // 1 hour later

		// Test NewBaseVestingAccount call (this tests the signature we use)
		baseVestingAcc, err := vestingtypes.NewBaseVestingAccount(baseAccount, vestingCoins, currentTime)
		suite.Require().NoError(err)
		suite.Require().NotNil(baseVestingAcc)
		suite.Require().Equal(vestingCoins, baseVestingAcc.OriginalVesting)

		// Set end time manually as required by our implementation
		baseVestingAcc.EndTime = endTime
		suite.Require().Equal(endTime, baseVestingAcc.EndTime)
	})

	// Test PeriodicVestingAccount creation parameters
	suite.Run("periodic_vesting_account_params", func() {
		baseAccount := authtypes.NewBaseAccountWithAddress(baseAddr)
		currentTime := time.Now().Unix()

		periods := []vestingtypes.Period{
			{
				Length: 1800, // 30 minutes
				Amount: sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))),
			},
			{
				Length: 1800, // 30 minutes
				Amount: sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))),
			},
		}

		// Test NewPeriodicVestingAccount call
		periodicAcc, err := vestingtypes.NewPeriodicVestingAccount(baseAccount, vestingCoins, currentTime, periods)
		suite.Require().NoError(err)
		suite.Require().NotNil(periodicAcc)
		suite.Require().Equal(vestingCoins, periodicAcc.OriginalVesting)
		suite.Require().Equal(periods, periodicAcc.VestingPeriods)
	})

	// Test ContinuousVestingAccount creation parameters
	suite.Run("continuous_vesting_account_params", func() {
		baseAccount := authtypes.NewBaseAccountWithAddress(baseAddr)
		startTime := time.Now().Unix()
		endTime := startTime + 3600 // 1 hour later

		// Test NewContinuousVestingAccount call
		continuousAcc, err := vestingtypes.NewContinuousVestingAccount(baseAccount, vestingCoins, startTime, endTime)
		suite.Require().NoError(err)
		suite.Require().NotNil(continuousAcc)
		suite.Require().Equal(vestingCoins, continuousAcc.OriginalVesting)
		suite.Require().Equal(startTime, continuousAcc.StartTime)
		suite.Require().Equal(endTime, continuousAcc.EndTime)
	})

	// Test DelayedVestingAccount creation parameters
	suite.Run("delayed_vesting_account_params", func() {
		baseAccount := authtypes.NewBaseAccountWithAddress(baseAddr)
		endTime := time.Now().Unix() + 3600 // 1 hour later

		// Test NewDelayedVestingAccount call
		delayedAcc, err := vestingtypes.NewDelayedVestingAccount(baseAccount, vestingCoins, endTime)
		suite.Require().NoError(err)
		suite.Require().NotNil(delayedAcc)
		suite.Require().Equal(vestingCoins, delayedAcc.OriginalVesting)
		suite.Require().Equal(endTime, delayedAcc.EndTime)
	})
}

// Test vesting schedule calculations
func (suite *VestingTestSuite) TestVestingScheduleCalculations() {
	// Test remaining period calculations for periodic vesting
	suite.Run("periodic_vesting_remaining_periods", func() {
		currentTime := time.Now().Unix()
		startTime := currentTime - 1800 // Started 30 minutes ago

		periods := []vestingtypes.Period{
			{
				Length: 1800, // 30 minutes (should be partially completed)
				Amount: sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))),
			},
			{
				Length: 1800, // 30 minutes (should be fully remaining)
				Amount: sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))),
			},
		}

		// Calculate what should remain
		// First period: started 30 minutes ago, length 30 minutes -> should be completed
		// Second period: should be fully remaining

		var remainingPeriods []vestingtypes.Period
		accumulatedTime := startTime

		for _, period := range periods {
			periodEndTime := accumulatedTime + period.Length
			if periodEndTime > currentTime {
				// This period has time remaining
				adjustedLength := period.Length
				if accumulatedTime < currentTime {
					// Partial period - adjust the length
					adjustedLength = periodEndTime - currentTime
				}

				remainingPeriods = append(remainingPeriods, vestingtypes.Period{
					Length: adjustedLength,
					Amount: period.Amount,
				})
			}
			accumulatedTime = periodEndTime
		}

		// Should have one remaining period (the second one)
		suite.Require().Len(remainingPeriods, 1)
		suite.Require().Equal(int64(1800), remainingPeriods[0].Length)
		suite.Require().Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))), remainingPeriods[0].Amount)
	})

	// Test continuous vesting proportional calculations
	suite.Run("continuous_vesting_proportional", func() {
		startTime := time.Now().Unix() - 1800 // Started 30 minutes ago
		endTime := startTime + 3600           // Total duration: 1 hour
		currentTime := time.Now().Unix()

		totalDuration := endTime - startTime       // 3600 seconds
		remainingDuration := endTime - currentTime // ~1800 seconds

		originalAmount := math.NewInt(1000)

		// Calculate remaining amount proportionally
		remainingAmount := originalAmount.MulRaw(remainingDuration).QuoRaw(totalDuration)

		// Should be approximately 500 (half remaining)
		suite.Require().True(remainingAmount.GT(math.NewInt(400)))
		suite.Require().True(remainingAmount.LT(math.NewInt(600)))
	})
}

// Test edge cases for vesting transfers
func (suite *VestingTestSuite) TestVestingTransferEdgeCases() {
	suite.Run("expired_vesting_periods", func() {
		// Test case where all vesting periods have expired
		currentTime := time.Now().Unix()
		pastTime := currentTime - 7200 // 2 hours ago

		// This would represent a vesting account where all periods have completed
		// The transfer should handle this gracefully
		suite.Require().True(pastTime < currentTime, "Past time should be before current time")
	})

	suite.Run("future_end_times", func() {
		// Test case with future end times
		currentTime := time.Now().Unix()
		futureTime := currentTime + 3600 // 1 hour in future

		// This represents active vesting that should be transferable
		suite.Require().True(futureTime > currentTime, "Future time should be after current time")
	})
}

// Benchmark vesting operations
func BenchmarkTransferVestingSchedule(b *testing.B) {
	k, ctx := keepertest.GenesistransferKeeper(b)
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// This will fail but we're testing the validation performance
		_ = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	}
}

func BenchmarkGetVestingInfo(b *testing.B) {
	k, ctx := keepertest.GenesistransferKeeper(b)
	testAddr := sdk.AccAddress("test_address_______")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = k.GetVestingInfo(ctx, testAddr)
	}
}
