package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/suite"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/genesistransfer/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

type WhitelistTestSuite struct {
	suite.Suite
	keeper keeper.Keeper
	ctx    sdk.Context
}

func (suite *WhitelistTestSuite) SetupTest() {
	k, ctx := keepertest.GenesistransferKeeper(suite.T())
	suite.keeper = k
	suite.ctx = ctx
}

// resetWhitelist resets the whitelist to default state for test isolation
func (suite *WhitelistTestSuite) resetWhitelist() {
	// Reset to default params (empty whitelist, disabled)
	params := types.NewParams([]string{}, false) // Explicitly create empty whitelist
	err := suite.keeper.SetParams(suite.ctx, params)
	suite.Require().NoError(err)

	// Verify the reset worked
	size := suite.keeper.GetWhitelistSize(suite.ctx)
	suite.Require().Equal(0, size, "whitelist should be empty after reset")
	suite.Require().False(suite.keeper.IsWhitelistEnabled(suite.ctx), "whitelist should be disabled after reset")
}

func TestWhitelistTestSuite(t *testing.T) {
	suite.Run(t, new(WhitelistTestSuite))
}

// Test whitelist enable/disable functionality
func (suite *WhitelistTestSuite) TestWhitelistEnableDisable() {
	suite.Run("default_state", func() {
		// Default state should be disabled
		enabled := suite.keeper.IsWhitelistEnabled(suite.ctx)
		suite.Require().False(enabled)
	})

	suite.Run("enable_whitelist", func() {
		err := suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)

		enabled := suite.keeper.IsWhitelistEnabled(suite.ctx)
		suite.Require().True(enabled)
	})

	suite.Run("disable_whitelist", func() {
		// First enable it
		err := suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)
		suite.Require().True(suite.keeper.IsWhitelistEnabled(suite.ctx))

		// Then disable it
		err = suite.keeper.DisableWhitelist(suite.ctx)
		suite.Require().NoError(err)

		enabled := suite.keeper.IsWhitelistEnabled(suite.ctx)
		suite.Require().False(enabled)
	})
}

// Test whitelist size and account management
func (suite *WhitelistTestSuite) TestWhitelistSize() {
	suite.Run("empty_whitelist", func() {
		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(0, size)
	})

	suite.Run("whitelist_with_accounts", func() {
		// Add some accounts to the whitelist
		accounts := []string{
			testutil.Creator,
			testutil.Requester,
			testutil.Executor,
		}

		params := types.NewParams(accounts, false)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(3, size)
	})
}

// Test IsAccountWhitelisted functionality
func (suite *WhitelistTestSuite) TestIsAccountWhitelisted() {
	testAddr := testutil.Creator
	otherAddr := testutil.Requester

	suite.Run("whitelist_disabled", func() {
		// When whitelist is disabled, all accounts are considered "whitelisted"
		isWhitelisted := suite.keeper.IsAccountWhitelisted(suite.ctx, testAddr)
		suite.Require().True(isWhitelisted)
	})

	suite.Run("whitelist_enabled_empty_list", func() {
		// Enable whitelist with empty list
		err := suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)

		isWhitelisted := suite.keeper.IsAccountWhitelisted(suite.ctx, testAddr)
		suite.Require().False(isWhitelisted)
	})

	suite.Run("whitelist_enabled_address_in_list", func() {
		// Add address to whitelist
		params := types.NewParams([]string{testAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isWhitelisted := suite.keeper.IsAccountWhitelisted(suite.ctx, testAddr)
		suite.Require().True(isWhitelisted)

		// Test address not in list
		isWhitelisted = suite.keeper.IsAccountWhitelisted(suite.ctx, otherAddr)
		suite.Require().False(isWhitelisted)
	})
}

// Test AddAccountsToWhitelist functionality
func (suite *WhitelistTestSuite) TestAddAccountsToWhitelist() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester
	addr3 := testutil.Executor

	suite.Run("add_empty_list", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{})
		suite.Require().NoError(err)

		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(0, size)
	})

	suite.Run("add_valid_addresses", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, addr2})
		suite.Require().NoError(err)

		// Enable whitelist to enforce membership checks
		err = suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)

		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(2, size)

		// Verify addresses are in the list
		suite.Require().True(suite.keeper.IsAccountWhitelisted(suite.ctx, addr1))
		suite.Require().True(suite.keeper.IsAccountWhitelisted(suite.ctx, addr2))
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr3))
	})

	suite.Run("add_duplicate_addresses", func() {
		suite.resetWhitelist() // Reset state for test isolation
		// First add some addresses
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, addr2})
		suite.Require().NoError(err)

		// Try to add duplicates
		err = suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, addr2})
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "already in the whitelist")
	})

	suite.Run("add_invalid_address", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{"invalid_address"})
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "invalid address")
	})

	suite.Run("add_mixed_valid_invalid", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, "invalid_address"})
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "invalid address")

		// Verify no addresses were added
		// Enable whitelist so non-membership is enforced
		err = suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr1))
	})
}

// Test RemoveAccountsFromWhitelist functionality
func (suite *WhitelistTestSuite) TestRemoveAccountsFromWhitelist() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester
	addr3 := testutil.Executor

	suite.Run("remove_empty_list", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.RemoveAccountsFromWhitelist(suite.ctx, []string{})
		suite.Require().NoError(err)
	})

	suite.Run("remove_from_empty_whitelist", func() {
		suite.resetWhitelist() // Reset state for test isolation
		err := suite.keeper.RemoveAccountsFromWhitelist(suite.ctx, []string{addr1})
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "none of the provided addresses were found in the whitelist")
	})

	suite.Run("remove_existing_addresses", func() {
		suite.resetWhitelist() // Reset state for test isolation
		// First add some addresses
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, addr2, addr3})
		suite.Require().NoError(err)
		suite.Require().Equal(3, suite.keeper.GetWhitelistSize(suite.ctx))

		// Remove some addresses
		err = suite.keeper.RemoveAccountsFromWhitelist(suite.ctx, []string{addr1, addr3})
		suite.Require().NoError(err)

		// Verify removal
		suite.Require().Equal(1, suite.keeper.GetWhitelistSize(suite.ctx))
		// Enable whitelist to enforce membership checks
		err = suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr1))
		suite.Require().True(suite.keeper.IsAccountWhitelisted(suite.ctx, addr2))
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr3))
	})

	suite.Run("remove_non_existent_addresses", func() {
		suite.resetWhitelist() // Reset state for test isolation
		// Set up whitelist with addr1
		params := types.NewParams([]string{addr1}, false)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		// Try to remove addr2 which is not in the list
		err = suite.keeper.RemoveAccountsFromWhitelist(suite.ctx, []string{addr2})
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "none of the provided addresses were found in the whitelist")
	})
}

// Test ClearWhitelist functionality
func (suite *WhitelistTestSuite) TestClearWhitelist() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester

	suite.Run("clear_empty_whitelist", func() {
		err := suite.keeper.ClearWhitelist(suite.ctx)
		suite.Require().NoError(err)

		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(0, size)
	})

	suite.Run("clear_populated_whitelist", func() {
		// Add some addresses
		err := suite.keeper.AddAccountsToWhitelist(suite.ctx, []string{addr1, addr2})
		suite.Require().NoError(err)
		suite.Require().Equal(2, suite.keeper.GetWhitelistSize(suite.ctx))

		// Clear the whitelist
		err = suite.keeper.ClearWhitelist(suite.ctx)
		suite.Require().NoError(err)

		// Verify it's empty
		size := suite.keeper.GetWhitelistSize(suite.ctx)
		suite.Require().Equal(0, size)
		// Enable whitelist so membership is enforced (otherwise IsAccountWhitelisted returns true)
		err = suite.keeper.EnableWhitelist(suite.ctx)
		suite.Require().NoError(err)
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr1))
		suite.Require().False(suite.keeper.IsAccountWhitelisted(suite.ctx, addr2))
	})
}

// Test GetWhitelistStats functionality
func (suite *WhitelistTestSuite) TestGetWhitelistStats() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester

	suite.Run("empty_whitelist_disabled", func() {
		stats := suite.keeper.GetWhitelistStats(suite.ctx)
		suite.Require().False(stats.Enabled)
		suite.Require().Equal(0, stats.AccountCount)
		suite.Require().Empty(stats.Accounts)
	})

	suite.Run("populated_whitelist_enabled", func() {
		// Set up whitelist
		params := types.NewParams([]string{addr1, addr2}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		stats := suite.keeper.GetWhitelistStats(suite.ctx)
		suite.Require().True(stats.Enabled)
		suite.Require().Equal(2, stats.AccountCount)
		suite.Require().Len(stats.Accounts, 2)
		suite.Require().Contains(stats.Accounts, addr1)
		suite.Require().Contains(stats.Accounts, addr2)
	})
}

// Test ValidateWhitelistConfiguration functionality
func (suite *WhitelistTestSuite) TestValidateWhitelistConfiguration() {
	suite.Run("valid_configuration", func() {
		// Set up valid configuration
		addr1 := testutil.Creator
		params := types.NewParams([]string{addr1}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		err = suite.keeper.ValidateWhitelistConfiguration(suite.ctx)
		suite.Require().NoError(err)
	})

	suite.Run("enabled_with_empty_list", func() {
		// Enable whitelist with empty list (should generate warning but not error)
		params := types.NewParams([]string{}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		err = suite.keeper.ValidateWhitelistConfiguration(suite.ctx)
		suite.Require().NoError(err) // Should not error, just warn
	})
}

// Test GetWhitelistDifference functionality
func (suite *WhitelistTestSuite) TestGetWhitelistDifference() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester
	addr3 := testutil.Executor
	addr4 := testutil.Validator

	suite.Run("no_changes", func() {
		current := []string{addr1, addr2}
		proposed := []string{addr1, addr2}

		toAdd, toRemove := suite.keeper.GetWhitelistDifference(current, proposed)
		suite.Require().Empty(toAdd)
		suite.Require().Empty(toRemove)
	})

	suite.Run("add_addresses", func() {
		current := []string{addr1}
		proposed := []string{addr1, addr2, addr3}

		toAdd, toRemove := suite.keeper.GetWhitelistDifference(current, proposed)
		suite.Require().Len(toAdd, 2)
		suite.Require().Contains(toAdd, addr2)
		suite.Require().Contains(toAdd, addr3)
		suite.Require().Empty(toRemove)
	})

	suite.Run("remove_addresses", func() {
		current := []string{addr1, addr2, addr3}
		proposed := []string{addr1}

		toAdd, toRemove := suite.keeper.GetWhitelistDifference(current, proposed)
		suite.Require().Empty(toAdd)
		suite.Require().Len(toRemove, 2)
		suite.Require().Contains(toRemove, addr2)
		suite.Require().Contains(toRemove, addr3)
	})

	suite.Run("add_and_remove", func() {
		current := []string{addr1, addr2}
		proposed := []string{addr2, addr3, addr4}

		toAdd, toRemove := suite.keeper.GetWhitelistDifference(current, proposed)
		suite.Require().Len(toAdd, 2)
		suite.Require().Contains(toAdd, addr3)
		suite.Require().Contains(toAdd, addr4)
		suite.Require().Len(toRemove, 1)
		suite.Require().Contains(toRemove, addr1)
	})
}

// Test UpdateWhitelist functionality
func (suite *WhitelistTestSuite) TestUpdateWhitelist() {
	addr1 := testutil.Creator
	addr2 := testutil.Requester
	addr3 := testutil.Executor

	suite.Run("update_empty_to_populated", func() {
		newAccounts := []string{addr1, addr2}
		err := suite.keeper.UpdateWhitelist(suite.ctx, newAccounts)
		suite.Require().NoError(err)

		stats := suite.keeper.GetWhitelistStats(suite.ctx)
		suite.Require().Equal(2, stats.AccountCount)
		suite.Require().Contains(stats.Accounts, addr1)
		suite.Require().Contains(stats.Accounts, addr2)
	})

	suite.Run("update_with_duplicates", func() {
		// Update with duplicates - should remove duplicates
		newAccounts := []string{addr1, addr2, addr1, addr3, addr2}
		err := suite.keeper.UpdateWhitelist(suite.ctx, newAccounts)
		suite.Require().NoError(err)

		stats := suite.keeper.GetWhitelistStats(suite.ctx)
		suite.Require().Equal(3, stats.AccountCount)
		suite.Require().Contains(stats.Accounts, addr1)
		suite.Require().Contains(stats.Accounts, addr2)
		suite.Require().Contains(stats.Accounts, addr3)
	})

	suite.Run("update_with_invalid_address", func() {
		newAccounts := []string{addr1, "invalid_address", addr2}
		err := suite.keeper.UpdateWhitelist(suite.ctx, newAccounts)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "invalid address")
	})

	suite.Run("update_to_empty", func() {
		// First populate the whitelist
		err := suite.keeper.UpdateWhitelist(suite.ctx, []string{addr1, addr2})
		suite.Require().NoError(err)
		suite.Require().Equal(2, suite.keeper.GetWhitelistSize(suite.ctx))

		// Then update to empty
		err = suite.keeper.UpdateWhitelist(suite.ctx, []string{})
		suite.Require().NoError(err)

		stats := suite.keeper.GetWhitelistStats(suite.ctx)
		suite.Require().Equal(0, stats.AccountCount)
		suite.Require().Empty(stats.Accounts)
	})
}
