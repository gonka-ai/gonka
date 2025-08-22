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

type ValidationTestSuite struct {
	suite.Suite
	keeper keeper.Keeper
	ctx    sdk.Context
}

func (suite *ValidationTestSuite) SetupTest() {
	k, ctx := keepertest.GenesistransferKeeper(suite.T())
	suite.keeper = k
	suite.ctx = ctx
}

func TestValidationTestSuite(t *testing.T) {
	suite.Run(t, new(ValidationTestSuite))
}

// Test ValidateTransfer comprehensive validation
func (suite *ValidationTestSuite) TestValidateTransfer() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	suite.Run("invalid_genesis_address", func() {
		err := suite.keeper.ValidateTransfer(suite.ctx, nil, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")
	})

	suite.Run("invalid_recipient_address", func() {
		err := suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "recipient address cannot be nil")
	})

	suite.Run("self_transfer", func() {
		err := suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, genesisAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "cannot transfer to the same address")
	})

	suite.Run("non_existent_genesis_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		err := suite.keeper.ValidateTransfer(suite.ctx, nonExistentAddr, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})
}

// Test ValidateTransferEligibility detailed validation
func (suite *ValidationTestSuite) TestValidateTransferEligibility() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")

	suite.Run("nil_address", func() {
		isEligible, reason, alreadyTransferred, err := suite.keeper.ValidateTransferEligibility(suite.ctx, nil)
		suite.Require().NoError(err)
		suite.Require().False(isEligible)
		suite.Require().Contains(reason, "genesis address cannot be nil")
		suite.Require().False(alreadyTransferred)
	})

	suite.Run("non_existent_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		isEligible, reason, alreadyTransferred, err := suite.keeper.ValidateTransferEligibility(suite.ctx, nonExistentAddr)
		suite.Require().NoError(err)
		suite.Require().False(isEligible)
		suite.Require().Contains(reason, "does not exist")
		suite.Require().False(alreadyTransferred)
	})

	// Test with already transferred account
	suite.Run("already_transferred", func() {
		// First set up a transfer record
		record := types.TransferRecord{
			GenesisAddress:   genesisAddr.String(),
			RecipientAddress: testutil.Requester,
			TransferHeight:   uint64(100),
			Completed:        true,
		}
		err := suite.keeper.SetTransferRecord(suite.ctx, record)
		suite.Require().NoError(err)

		// Now check eligibility
		isEligible, reason, alreadyTransferred, err := suite.keeper.ValidateTransferEligibility(suite.ctx, genesisAddr)
		suite.Require().NoError(err)
		suite.Require().False(isEligible)
		suite.Require().Contains(reason, "already been transferred")
		suite.Require().True(alreadyTransferred)
	})
}

// Test IsTransferableAccount whitelist validation
func (suite *ValidationTestSuite) TestIsTransferableAccount() {
	testAddr := testutil.Creator

	suite.Run("whitelist_disabled", func() {
		// Default params have whitelist disabled
		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, testAddr)
		suite.Require().True(isTransferable)
	})

	suite.Run("whitelist_enabled_empty_list", func() {
		// Enable whitelist with empty list
		params := types.NewParams([]string{}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, testAddr)
		suite.Require().False(isTransferable)
	})

	suite.Run("whitelist_enabled_address_in_list", func() {
		// Enable whitelist with test address
		params := types.NewParams([]string{testAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, testAddr)
		suite.Require().True(isTransferable)
	})

	suite.Run("whitelist_enabled_address_not_in_list", func() {
		// Enable whitelist with different address
		otherAddr := testutil.Requester
		params := types.NewParams([]string{otherAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, testAddr)
		suite.Require().False(isTransferable)
	})

	suite.Run("whitelist_enabled_multiple_addresses", func() {
		// Enable whitelist with multiple addresses
		addr1 := testutil.Creator
		addr2 := testutil.Requester
		addr3 := testutil.Executor

		params := types.NewParams([]string{addr1, addr2, addr3}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		// Test each address
		suite.Require().True(suite.keeper.IsTransferableAccount(suite.ctx, addr1))
		suite.Require().True(suite.keeper.IsTransferableAccount(suite.ctx, addr2))
		suite.Require().True(suite.keeper.IsTransferableAccount(suite.ctx, addr3))

		// Test non-listed address
		otherAddr := testutil.Validator
		suite.Require().False(suite.keeper.IsTransferableAccount(suite.ctx, otherAddr))
	})
}

// Test GetTransferRecord and SetTransferRecord
func (suite *ValidationTestSuite) TestTransferRecordOperations() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	suite.Run("get_non_existent_record", func() {
		record, found, err := suite.keeper.GetTransferRecord(suite.ctx, genesisAddr)
		suite.Require().NoError(err)
		suite.Require().False(found)
		suite.Require().Nil(record)
	})

	suite.Run("get_record_nil_address", func() {
		record, found, err := suite.keeper.GetTransferRecord(suite.ctx, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")
		suite.Require().False(found)
		suite.Require().Nil(record)
	})

	suite.Run("set_valid_record", func() {
		record := types.TransferRecord{
			GenesisAddress:    genesisAddr.String(),
			RecipientAddress:  recipientAddr.String(),
			TransferHeight:    uint64(100),
			Completed:         true,
			TransferredDenoms: []string{"stake", "token"},
			TransferAmount:    "1000stake,500token",
		}

		err := suite.keeper.SetTransferRecord(suite.ctx, record)
		suite.Require().NoError(err)

		// Verify record was stored correctly
		storedRecord, found, err := suite.keeper.GetTransferRecord(suite.ctx, genesisAddr)
		suite.Require().NoError(err)
		suite.Require().True(found)
		suite.Require().NotNil(storedRecord)
		suite.Require().Equal(record.GenesisAddress, storedRecord.GenesisAddress)
		suite.Require().Equal(record.RecipientAddress, storedRecord.RecipientAddress)
		suite.Require().Equal(record.TransferHeight, storedRecord.TransferHeight)
		suite.Require().Equal(record.Completed, storedRecord.Completed)
		suite.Require().Equal(record.TransferredDenoms, storedRecord.TransferredDenoms)
		suite.Require().Equal(record.TransferAmount, storedRecord.TransferAmount)
	})

	suite.Run("set_invalid_record_bad_genesis_address", func() {
		invalidRecord := types.TransferRecord{
			GenesisAddress:   "invalid_address",
			RecipientAddress: recipientAddr.String(),
			TransferHeight:   uint64(100),
			Completed:        true,
		}

		err := suite.keeper.SetTransferRecord(suite.ctx, invalidRecord)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "invalid genesis address")
	})

	suite.Run("set_invalid_record_bad_recipient_address", func() {
		invalidRecord := types.TransferRecord{
			GenesisAddress:   genesisAddr.String(),
			RecipientAddress: "invalid_address",
			TransferHeight:   uint64(100),
			Completed:        true,
		}

		err := suite.keeper.SetTransferRecord(suite.ctx, invalidRecord)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "invalid recipient address")
	})

	suite.Run("set_invalid_record_zero_height", func() {
		invalidRecord := types.TransferRecord{
			GenesisAddress:   genesisAddr.String(),
			RecipientAddress: recipientAddr.String(),
			TransferHeight:   uint64(0),
			Completed:        true,
		}

		err := suite.keeper.SetTransferRecord(suite.ctx, invalidRecord)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "transfer height cannot be zero")
	})
}

// Test HasTransferRecord
func (suite *ValidationTestSuite) TestHasTransferRecord() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	suite.Run("non_existent_record", func() {
		hasRecord := suite.keeper.HasTransferRecord(suite.ctx, genesisAddr)
		suite.Require().False(hasRecord)
	})

	suite.Run("nil_address", func() {
		hasRecord := suite.keeper.HasTransferRecord(suite.ctx, nil)
		suite.Require().False(hasRecord)
	})

	suite.Run("existing_record", func() {
		// Set up a record first
		record := types.TransferRecord{
			GenesisAddress:   genesisAddr.String(),
			RecipientAddress: recipientAddr.String(),
			TransferHeight:   uint64(100),
			Completed:        true,
		}
		err := suite.keeper.SetTransferRecord(suite.ctx, record)
		suite.Require().NoError(err)

		// Now check if it exists
		hasRecord := suite.keeper.HasTransferRecord(suite.ctx, genesisAddr)
		suite.Require().True(hasRecord)
	})
}

// Test validation with whitelist scenarios
func (suite *ValidationTestSuite) TestValidationWithWhitelist() {
	// Use valid bech32 addresses for whitelist testing
	validGenesisAddr := testutil.Creator
	validRecipientAddr := testutil.Requester

	suite.Run("whitelist_disabled_should_pass", func() {
		// Ensure whitelist is disabled
		params := types.NewParams([]string{}, false)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		// Validation should not fail due to whitelist
		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, validGenesisAddr)
		suite.Require().True(isTransferable)
	})

	suite.Run("whitelist_enabled_address_in_list", func() {
		// Enable whitelist with genesis address
		params := types.NewParams([]string{validGenesisAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, validGenesisAddr)
		suite.Require().True(isTransferable)
	})

	suite.Run("whitelist_enabled_address_not_in_list", func() {
		// Enable whitelist with different address
		params := types.NewParams([]string{validRecipientAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, validGenesisAddr)
		suite.Require().False(isTransferable)
	})
}

// Test edge cases and error conditions
func (suite *ValidationTestSuite) TestValidationEdgeCases() {
	suite.Run("empty_string_address", func() {
		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, "")
		// Empty string should be handled gracefully
		suite.Require().False(isTransferable)
	})

	suite.Run("malformed_address", func() {
		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, "not_a_valid_address")
		// Malformed address should be handled gracefully
		suite.Require().False(isTransferable)
	})

	suite.Run("very_long_address", func() {
		longAddr := "cosmos1" + string(make([]byte, 1000)) // Very long string
		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, longAddr)
		// Should handle gracefully
		suite.Require().False(isTransferable)
	})
}

// Benchmark validation functions
func BenchmarkValidateTransferEligibility(b *testing.B) {
	k, ctx := keepertest.GenesistransferKeeper(b)
	genesisAddr := sdk.AccAddress("genesis_addr_______")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = k.ValidateTransferEligibility(ctx, genesisAddr)
	}
}

func BenchmarkHasTransferRecord(b *testing.B) {
	k, ctx := keepertest.GenesistransferKeeper(b)
	genesisAddr := sdk.AccAddress("genesis_addr_______")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = k.HasTransferRecord(ctx, genesisAddr)
	}
}
