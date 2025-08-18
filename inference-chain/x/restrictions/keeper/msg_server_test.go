package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

func setupMsgServer(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.RestrictionsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func TestMsgServer(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	require.NotNil(t, ms)
	require.NotNil(t, ctx)
	require.NotEmpty(t, k)
}

// TestMsgServer_UpdateParams_Authority tests parameter updates with different authorities
func TestMsgServer_UpdateParams_Authority(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Test valid authority
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000

	msg := &types.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    params,
	}

	_, err := ms.UpdateParams(ctx, msg)
	require.NoError(t, err)

	// Verify parameters were updated
	updatedParams := k.GetParams(sdk.UnwrapSDKContext(ctx))
	require.Equal(t, uint64(2000000), updatedParams.RestrictionEndBlock)

	// Test invalid authority
	invalidMsg := &types.MsgUpdateParams{
		Authority: "invalid-authority",
		Params:    params,
	}

	_, err = ms.UpdateParams(ctx, invalidMsg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid authority")
}

// TestMsgServer_UpdateParams_ParameterValidation tests parameter validation in updates
func TestMsgServer_UpdateParams_ParameterValidation(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	testCases := []struct {
		name      string
		params    types.Params
		expectErr bool
	}{
		{
			name:      "valid default parameters",
			params:    types.DefaultParams(),
			expectErr: false,
		},
		{
			name: "valid custom parameters",
			params: types.Params{
				RestrictionEndBlock: 1000000,
				EmergencyTransferExemptions: []types.EmergencyTransferExemption{
					{
						ExemptionId:   "test-exemption",
						FromAddress:   testutil.Creator,
						ToAddress:     testutil.Requester,
						MaxAmount:     "1000",
						UsageLimit:    5,
						ExpiryBlock:   900000,
						Justification: "Test exemption",
					},
				},
				ExemptionUsageTracking: []types.ExemptionUsage{},
			},
			expectErr: false,
		},
		{
			name: "parameters with multiple exemptions",
			params: types.Params{
				RestrictionEndBlock: 2000000,
				EmergencyTransferExemptions: []types.EmergencyTransferExemption{
					{
						ExemptionId:   "exemption-1",
						FromAddress:   testutil.Creator,
						ToAddress:     testutil.Requester,
						MaxAmount:     "1000",
						UsageLimit:    3,
						ExpiryBlock:   1500000,
						Justification: "First exemption",
					},
					{
						ExemptionId:   "exemption-2",
						FromAddress:   testutil.Executor,
						ToAddress:     "*", // Wildcard
						MaxAmount:     "2000",
						UsageLimit:    5,
						ExpiryBlock:   1800000,
						Justification: "Second exemption",
					},
				},
				ExemptionUsageTracking: []types.ExemptionUsage{},
			},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			msg := &types.MsgUpdateParams{
				Authority: k.GetAuthority(),
				Params:    tc.params,
			}

			_, err := ms.UpdateParams(ctx, msg)

			if tc.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify parameters were set correctly
				updatedParams := k.GetParams(sdk.UnwrapSDKContext(ctx))
				require.Equal(t, tc.params.RestrictionEndBlock, updatedParams.RestrictionEndBlock)
				require.Equal(t, len(tc.params.EmergencyTransferExemptions), len(updatedParams.EmergencyTransferExemptions))
			}
		})
	}
}

// TestMsgServer_EmergencyTransfer_Integration tests emergency transfer message integration with parameters
func TestMsgServer_EmergencyTransfer_Integration(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Set up parameters with emergency exemption via UpdateParams
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000
	exemption := types.EmergencyTransferExemption{
		ExemptionId:   "integration-test",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1500",
		UsageLimit:    3,
		ExpiryBlock:   1500000,
		Justification: "Integration test exemption",
	}
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption}

	// Update parameters via message server
	updateMsg := &types.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    params,
	}

	_, err := ms.UpdateParams(ctx, updateMsg)
	require.NoError(t, err)

	// Set block height for active restrictions
	ctx = context.WithValue(ctx, sdk.SdkContextKey, sdk.UnwrapSDKContext(ctx).WithBlockHeight(100000))

	// Execute emergency transfer via message server
	transferMsg := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "integration-test",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "1000",
		Denom:       "ugonka",
	}

	resp, err := ms.ExecuteEmergencyTransfer(ctx, transferMsg)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(2), resp.RemainingUses) // 3 - 1 = 2

	// Verify usage tracking was updated
	updatedParams := k.GetParams(sdk.UnwrapSDKContext(ctx))
	require.Len(t, updatedParams.ExemptionUsageTracking, 1)
	require.Equal(t, "integration-test", updatedParams.ExemptionUsageTracking[0].ExemptionId)
	require.Equal(t, testutil.Creator, updatedParams.ExemptionUsageTracking[0].AccountAddress)
	require.Equal(t, uint64(1), updatedParams.ExemptionUsageTracking[0].UsageCount)
}

// TestMsgServer_EmergencyTransfer_CrossParameterUpdate tests emergency transfers across parameter updates
func TestMsgServer_EmergencyTransfer_CrossParameterUpdate(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Step 1: Set up initial parameters with exemption
	initialParams := types.DefaultParams()
	initialParams.RestrictionEndBlock = 2000000
	exemption1 := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption-1",
		FromAddress:   testutil.Creator,
		ToAddress:     testutil.Requester,
		MaxAmount:     "1000",
		UsageLimit:    2,
		ExpiryBlock:   1500000,
		Justification: "First exemption",
	}
	initialParams.EmergencyTransferExemptions = []types.EmergencyTransferExemption{exemption1}

	updateMsg1 := &types.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    initialParams,
	}

	_, err := ms.UpdateParams(ctx, updateMsg1)
	require.NoError(t, err)

	// Set block height for active restrictions
	ctx = context.WithValue(ctx, sdk.SdkContextKey, sdk.UnwrapSDKContext(ctx).WithBlockHeight(100000))

	// Step 2: Execute first emergency transfer
	transferMsg1 := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption-1",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "500",
		Denom:       "ugonka",
	}

	resp1, err := ms.ExecuteEmergencyTransfer(ctx, transferMsg1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp1.RemainingUses)

	// Step 3: Update parameters to add another exemption while preserving usage tracking
	updatedParams := k.GetParams(sdk.UnwrapSDKContext(ctx)) // Get current params with usage tracking
	exemption2 := types.EmergencyTransferExemption{
		ExemptionId:   "test-exemption-2",
		FromAddress:   testutil.Executor,
		ToAddress:     testutil.Requester,
		MaxAmount:     "2000",
		UsageLimit:    3,
		ExpiryBlock:   1600000,
		Justification: "Second exemption",
	}
	updatedParams.EmergencyTransferExemptions = append(updatedParams.EmergencyTransferExemptions, exemption2)

	updateMsg2 := &types.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    updatedParams,
	}

	_, err = ms.UpdateParams(ctx, updateMsg2)
	require.NoError(t, err)

	// Step 4: Execute transfer with second exemption
	transferMsg2 := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption-2",
		FromAddress: testutil.Executor,
		ToAddress:   testutil.Requester,
		Amount:      "1500",
		Denom:       "ugonka",
	}

	resp2, err := ms.ExecuteEmergencyTransfer(ctx, transferMsg2)
	require.NoError(t, err)
	require.Equal(t, uint64(2), resp2.RemainingUses) // 3 - 1 = 2

	// Step 5: Execute another transfer with first exemption (should still work)
	transferMsg3 := &types.MsgExecuteEmergencyTransfer{
		ExemptionId: "test-exemption-1",
		FromAddress: testutil.Creator,
		ToAddress:   testutil.Requester,
		Amount:      "300",
		Denom:       "ugonka",
	}

	resp3, err := ms.ExecuteEmergencyTransfer(ctx, transferMsg3)
	require.NoError(t, err)
	require.Equal(t, uint64(0), resp3.RemainingUses) // 1 - 1 = 0

	// Verify final usage tracking
	finalParams := k.GetParams(sdk.UnwrapSDKContext(ctx))
	require.Len(t, finalParams.ExemptionUsageTracking, 2)

	// Check individual usage counts
	creatorUsage := uint64(0)
	executorUsage := uint64(0)
	for _, usage := range finalParams.ExemptionUsageTracking {
		if usage.AccountAddress == testutil.Creator {
			creatorUsage = usage.UsageCount
		} else if usage.AccountAddress == testutil.Executor {
			executorUsage = usage.UsageCount
		}
	}

	require.Equal(t, uint64(2), creatorUsage)  // Used exemption-1 twice
	require.Equal(t, uint64(1), executorUsage) // Used exemption-2 once
}

// TestMsgServer_MessageValidation tests basic message validation for both message types
func TestMsgServer_MessageValidation(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	testCases := []struct {
		name      string
		msg       sdk.Msg
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid UpdateParams message",
			msg: &types.MsgUpdateParams{
				Authority: k.GetAuthority(),
				Params: types.Params{
					RestrictionEndBlock:         1000000, // Set to valid non-zero value for testing
					EmergencyTransferExemptions: []types.EmergencyTransferExemption{},
					ExemptionUsageTracking:      []types.ExemptionUsage{},
				},
			},
			expectErr: false,
		},
		{
			name: "invalid UpdateParams - empty authority",
			msg: &types.MsgUpdateParams{
				Authority: "",
				Params:    types.DefaultParams(),
			},
			expectErr: true,
			errMsg:    "invalid authority address",
		},
		{
			name: "valid ExecuteEmergencyTransfer message",
			msg: &types.MsgExecuteEmergencyTransfer{
				ExemptionId: "valid-exemption",
				FromAddress: testutil.Creator,
				ToAddress:   testutil.Requester,
				Amount:      "1000",
				Denom:       "ugonka",
			},
			expectErr: false, // ValidateBasic should pass, handler will check exemption existence
		},
		{
			name: "invalid ExecuteEmergencyTransfer - empty exemption ID",
			msg: &types.MsgExecuteEmergencyTransfer{
				ExemptionId: "",
				FromAddress: testutil.Creator,
				ToAddress:   testutil.Requester,
				Amount:      "1000",
				Denom:       "ugonka",
			},
			expectErr: true,
			errMsg:    "exemption ID cannot be empty",
		},
		{
			name: "invalid ExecuteEmergencyTransfer - invalid from address",
			msg: &types.MsgExecuteEmergencyTransfer{
				ExemptionId: "valid-exemption",
				FromAddress: "invalid-address",
				ToAddress:   testutil.Requester,
				Amount:      "1000",
				Denom:       "ugonka",
			},
			expectErr: true,
			errMsg:    "invalid from address",
		},
		{
			name: "invalid ExecuteEmergencyTransfer - empty amount",
			msg: &types.MsgExecuteEmergencyTransfer{
				ExemptionId: "valid-exemption",
				FromAddress: testutil.Creator,
				ToAddress:   testutil.Requester,
				Amount:      "",
				Denom:       "ugonka",
			},
			expectErr: true,
			errMsg:    "amount cannot be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test ValidateBasic and message handling
			switch msg := tc.msg.(type) {
			case *types.MsgUpdateParams:
				// Test ValidateBasic
				err := msg.ValidateBasic()
				if tc.expectErr {
					require.Error(t, err)
					require.Contains(t, err.Error(), tc.errMsg)
				} else {
					require.NoError(t, err)

					// Test message handler (should only succeed if ValidateBasic passed)
					_, err = ms.UpdateParams(ctx, msg)
					require.NoError(t, err)
				}
			case *types.MsgExecuteEmergencyTransfer:
				// Test ValidateBasic
				err := msg.ValidateBasic()
				if tc.expectErr {
					require.Error(t, err)
					require.Contains(t, err.Error(), tc.errMsg)
				} else {
					require.NoError(t, err)

					// Test message handler (will fail due to missing exemption, but that's expected)
					_, err := ms.ExecuteEmergencyTransfer(ctx, msg)
					require.Error(t, err) // Expected to fail due to missing exemption
					require.Contains(t, err.Error(), "not found")
				}
			}
		})
	}
}
