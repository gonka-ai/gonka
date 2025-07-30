package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestGranteesByMessageTypeQuery(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	tests := []struct {
		name        string
		req         *types.QueryGranteesByMessageTypeRequest
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil request",
			req:         nil,
			expectError: true,
			errorMsg:    "invalid request",
		},
		{
			name: "empty granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: true,
			errorMsg:    "granter address cannot be empty",
		},
		{
			name: "empty message type URL",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "cosmos1zxcv45xjkldf",
				MessageTypeUrl: "",
			},
			expectError: true,
			errorMsg:    "message type URL cannot be empty",
		},
		{
			name: "invalid granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "invalid-address",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: true,
			errorMsg:    "invalid granter address",
		},
		{
			name: "valid request with valid granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "cosmos1jmjfq0tplp9tmx4v9uemw72y4d2wa5nr3xn9d3",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := keeper.GranteesByMessageType(ctx, tt.req)

			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errorMsg)
				require.Nil(t, response)
			} else {
				require.NoError(t, err)
				require.NotNil(t, response)
				require.NotNil(t, response.GranteeAddresses)
				// For now, we expect empty results since this is a placeholder implementation
				require.Equal(t, 0, len(response.GranteeAddresses))
			}
		})
	}
}

func TestGranteesByMessageTypeQueryWithValidMessageTypes(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	validMessageTypes := []string{
		"/cosmos.bank.v1beta1.MsgSend",
		"/cosmos.staking.v1beta1.MsgDelegate",
		"/inference.inference.MsgStartInference",
		"/inference.inference.MsgFinishInference",
		"/inference.inference.MsgClaimRewards",
	}

	validGranterAddress := "cosmos1jmjfq0tplp9tmx4v9uemw72y4d2wa5nr3xn9d3"

	for _, msgType := range validMessageTypes {
		t.Run("message_type_"+msgType, func(t *testing.T) {
			req := &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: validGranterAddress,
				MessageTypeUrl: msgType,
			}

			response, err := keeper.GranteesByMessageType(ctx, req)

			require.NoError(t, err)
			require.NotNil(t, response)
			require.NotNil(t, response.GranteeAddresses)
			// For now, we expect empty results since this is a placeholder implementation
			require.Equal(t, 0, len(response.GranteeAddresses))
		})
	}
}
