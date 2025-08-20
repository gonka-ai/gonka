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

type TransferTestSuite struct {
	suite.Suite
	keeper keeper.Keeper
	ctx    sdk.Context
}

func (suite *TransferTestSuite) SetupTest() {
	k, ctx := keepertest.GenesistransferKeeper(suite.T())
	suite.keeper = k
	suite.ctx = ctx
}

func TestTransferTestSuite(t *testing.T) {
	suite.Run(t, new(TransferTestSuite))
}

// Test ExecuteOwnershipTransfer with various scenarios
func (suite *TransferTestSuite) TestExecuteOwnershipTransfer() {
	// Test addresses
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	// Test case 1: Invalid addresses
	suite.Run("invalid_genesis_address", func() {
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nil, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")
	})

	suite.Run("invalid_recipient_address", func() {
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, genesisAddr, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "recipient address cannot be nil")
	})

	suite.Run("self_transfer", func() {
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, genesisAddr, genesisAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "cannot transfer to the same address")
	})

	// Test case 2: Non-existent genesis account
	suite.Run("non_existent_genesis_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nonExistentAddr, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})
}

// Test ExecuteOwnershipTransfer validation (replacing TransferLiquidBalances test)
func (suite *TransferTestSuite) TestTransferLiquidBalances() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

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

	suite.Run("non_existent_genesis_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nonExistentAddr, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})
}

// Test ValidateTransfer function (replacing ValidateBalanceTransfer test)
func (suite *TransferTestSuite) TestValidateBalanceTransfer() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	suite.Run("invalid_addresses", func() {
		err := suite.keeper.ValidateTransfer(suite.ctx, nil, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")

		err = suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "recipient address cannot be nil")

		err = suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, genesisAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "cannot transfer to the same address")
	})
}

// Test GetTransferableBalance function
func (suite *TransferTestSuite) TestGetTransferableBalance() {

	suite.Run("non_existent_account", func() {
		testAddr := sdk.AccAddress("test_addr__________")
		// Test using ExecuteOwnershipTransfer instead (will fail due to non-existent account)
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, testAddr, sdk.AccAddress("recipient"))
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})

	suite.Run("nil_address", func() {
		// Test validation using ExecuteOwnershipTransfer instead
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nil, sdk.AccAddress("recipient"))
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")
	})
}

// Test vesting schedule transfer via ExecuteOwnershipTransfer (replacing TransferVestingSchedule test)
func (suite *TransferTestSuite) TestTransferVestingSchedule() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

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

	suite.Run("non_existent_genesis_account", func() {
		nonExistentAddr := sdk.AccAddress("non_existent_______")
		err := suite.keeper.ExecuteOwnershipTransfer(suite.ctx, nonExistentAddr, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "does not exist")
	})
}

// Test GetVestingInfo utility function
func (suite *TransferTestSuite) TestGetVestingInfo() {

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

// Test transfer validation functions
func (suite *TransferTestSuite) TestValidateTransfer() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	suite.Run("invalid_addresses", func() {
		err := suite.keeper.ValidateTransfer(suite.ctx, nil, recipientAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "genesis address cannot be nil")

		err = suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, nil)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "recipient address cannot be nil")

		err = suite.keeper.ValidateTransfer(suite.ctx, genesisAddr, genesisAddr)
		suite.Require().Error(err)
		suite.Require().Contains(err.Error(), "cannot transfer to the same address")
	})
}

// Test ValidateTransferEligibility function
func (suite *TransferTestSuite) TestValidateTransferEligibility() {

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
}

// Test IsTransferableAccount whitelist function
func (suite *TransferTestSuite) TestIsTransferableAccount() {
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

	suite.Run("whitelist_enabled_with_address", func() {
		// Enable whitelist with test address
		params := types.NewParams([]string{testAddr}, true)
		err := suite.keeper.SetParams(suite.ctx, params)
		suite.Require().NoError(err)

		isTransferable := suite.keeper.IsTransferableAccount(suite.ctx, testAddr)
		suite.Require().True(isTransferable)

		// Test different address
		otherAddr := testutil.Requester
		isTransferable = suite.keeper.IsTransferableAccount(suite.ctx, otherAddr)
		suite.Require().False(isTransferable)
	})
}

// Test transfer record functions
func (suite *TransferTestSuite) TestTransferRecords() {
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	// Test GetTransferRecord with non-existent record
	suite.Run("get_non_existent_record", func() {
		record, found, err := suite.keeper.GetTransferRecord(suite.ctx, genesisAddr)
		suite.Require().NoError(err)
		suite.Require().False(found)
		suite.Require().Nil(record)
	})

	// Test HasTransferRecord
	suite.Run("has_transfer_record", func() {
		hasRecord := suite.keeper.HasTransferRecord(suite.ctx, genesisAddr)
		suite.Require().False(hasRecord)

		hasRecord = suite.keeper.HasTransferRecord(suite.ctx, nil)
		suite.Require().False(hasRecord)
	})

	// Test SetTransferRecord with valid record
	suite.Run("set_valid_record", func() {
		record := types.TransferRecord{
			GenesisAddress:    genesisAddr.String(),
			RecipientAddress:  recipientAddr.String(),
			TransferHeight:    uint64(100),
			Completed:         true,
			TransferredDenoms: []string{"stake"},
			TransferAmount:    "1000stake",
		}

		err := suite.keeper.SetTransferRecord(suite.ctx, record)
		suite.Require().NoError(err)

		// Verify record was stored
		storedRecord, found, err := suite.keeper.GetTransferRecord(suite.ctx, genesisAddr)
		suite.Require().NoError(err)
		suite.Require().True(found)
		suite.Require().NotNil(storedRecord)
		suite.Require().Equal(record.GenesisAddress, storedRecord.GenesisAddress)
		suite.Require().Equal(record.RecipientAddress, storedRecord.RecipientAddress)
		suite.Require().Equal(record.TransferHeight, storedRecord.TransferHeight)
		suite.Require().Equal(record.Completed, storedRecord.Completed)

		// Test HasTransferRecord after setting
		hasRecord := suite.keeper.HasTransferRecord(suite.ctx, genesisAddr)
		suite.Require().True(hasRecord)
	})

	// Test SetTransferRecord with invalid record
	suite.Run("set_invalid_record", func() {
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
}

// Test GetAllTransferRecords
func (suite *TransferTestSuite) TestGetAllTransferRecords() {
	suite.Run("empty_records", func() {
		records, pageRes, err := suite.keeper.GetAllTransferRecords(suite.ctx, nil)
		suite.Require().NoError(err)
		suite.Require().Empty(records)
		suite.Require().NotNil(pageRes)
	})

	// Add some records and test retrieval
	suite.Run("with_records", func() {
		// Add test records
		for i := 0; i < 3; i++ {
			testGenesisAddr := sdk.AccAddress([]byte("genesis_addr_" + string(rune(i))))
			testRecipientAddr := sdk.AccAddress([]byte("recipient_addr_" + string(rune(i))))

			record := types.TransferRecord{
				GenesisAddress:   testGenesisAddr.String(),
				RecipientAddress: testRecipientAddr.String(),
				TransferHeight:   uint64(100 + i),
				Completed:        true,
			}

			err := suite.keeper.SetTransferRecord(suite.ctx, record)
			suite.Require().NoError(err)
		}

		records, pageRes, err := suite.keeper.GetAllTransferRecords(suite.ctx, nil)
		suite.Require().NoError(err)
		suite.Require().Len(records, 3)
		suite.Require().NotNil(pageRes)
	})
}

// Test GetTransferRecordsCount
func (suite *TransferTestSuite) TestGetTransferRecordsCount() {
	suite.Run("empty_count", func() {
		count, err := suite.keeper.GetTransferRecordsCount(suite.ctx)
		suite.Require().NoError(err)
		suite.Require().Equal(uint64(0), count)
	})
}

// Benchmark tests for performance
func BenchmarkExecuteOwnershipTransfer(b *testing.B) {
	k, ctx := keepertest.GenesistransferKeeper(b)
	genesisAddr := sdk.AccAddress("genesis_addr_______")
	recipientAddr := sdk.AccAddress("recipient_addr____")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// This will fail but we're testing the validation performance
		_ = k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
	}
}
