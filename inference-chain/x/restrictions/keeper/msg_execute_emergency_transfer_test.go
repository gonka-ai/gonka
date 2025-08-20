package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/restrictions/types"
)

func TestMsgExecuteEmergencyTransfer_Success(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create emergency exemption using valid test addresses
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption-1",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Test emergency transfer",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption-1",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Execute emergency transfer
	resp, err := ms.ExecuteEmergencyTransfer(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(4), resp.RemainingUses) // Usage limit 5 - 1 = 4
}

func TestMsgExecuteEmergencyTransfer_ExemptionNotFound(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)

	// Create test message with non-existent exemption
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "non-existent-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Should fail with exemption not found
	_, err := ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestMsgExecuteEmergencyTransfer_ExemptionExpired(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create expired exemption
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "expired-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   100, // Past block
		Justification: "Test expired exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Set current block to be after expiry
	ctx = context.WithValue(ctx, sdk.SdkContextKey, sdk.UnwrapSDKContext(ctx).WithBlockHeight(200))

	// Create test message
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "expired-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Should fail with exemption expired
	_, err = ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
}

func TestMsgExecuteEmergencyTransfer_AddressMismatch(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create exemption
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message with wrong from address
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption",
		FromAddress: testutil.Executor, // Different from exemption
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Should fail with address mismatch
	_, err = ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match")
}

func TestMsgExecuteEmergencyTransfer_AmountExceeded(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create exemption with max amount
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message with amount exceeding max
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "2000", // Exceeds max of 1000
		Denom:       "ugonka",
	}

	// Should fail with amount exceeded
	_, err = ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum")
}

func TestMsgExecuteEmergencyTransfer_UsageLimitExceeded(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create exemption with usage limit
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    2, // Low limit for testing
		ExpiryBlock:   1500000,
		Justification: "Test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	// Create usage tracking showing limit already reached
	usageTracking := types.ExemptionUsage{
		ExemptionId:    "test-exemption",
		AccountAddress: testutil.Creator,
		UsageCount:     2, // Already at limit
	}
	params.ExemptionUsageTracking = []types.ExemptionUsage{usageTracking}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Should fail with usage limit exceeded
	_, err = ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage limit exceeded")
}

func TestMsgExecuteEmergencyTransfer_WildcardAddresses(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create exemption with wildcard addresses
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "wildcard-exemption",
		FromAddress:   "*", // Wildcard from
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Wildcard test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message with any from address (should match wildcard)
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "wildcard-exemption",
		FromAddress: testutil.Executor, // Different address should work with wildcard
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	// Should succeed with wildcard match
	resp, err := ms.ExecuteEmergencyTransfer(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(4), resp.RemainingUses)
}

func TestMsgExecuteEmergencyTransfer_InvalidAmount(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up active restrictions and valid exemption first
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block

	// Create exemption so we can test amount validation
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    5,
		ExpiryBlock:   1500000,
		Justification: "Test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	err := k.SetParams(sdk.UnwrapSDKContext(ctx), params)
	require.NoError(t, err)

	// Create test message with invalid amount format
	msg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "invalid_amount",
		Denom:       "ugonka",
	}

	// Should fail with amount format error
	_, err = ms.ExecuteEmergencyTransfer(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid amount format")
}
