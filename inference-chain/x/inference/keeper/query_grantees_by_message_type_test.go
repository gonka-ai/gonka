package keeper_test

import (
	"bytes"
	"fmt"
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestGranteesByMessageTypeQuery(t *testing.T) {
	keeper, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	tests := []struct {
		name        string
		req         *types.QueryGranteesByMessageTypeRequest
		expectError bool
		errorMsg    string
		setupMock   func()
	}{
		{
			name:        "nil request",
			req:         nil,
			expectError: true,
			errorMsg:    "invalid request",
			setupMock:   func() {}, // No mock needed for nil request
		},
		{
			name: "empty granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: true,
			errorMsg:    "granter address cannot be empty",
			setupMock:   func() {}, // No mock needed for validation failure
		},
		{
			name: "empty message type URL",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "cosmos1zxcv45xjkldf",
				MessageTypeUrl: "",
			},
			expectError: true,
			errorMsg:    "message type URL cannot be empty",
			setupMock:   func() {}, // No mock needed for validation failure
		},
		{
			name: "invalid granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "invalid-address",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: true,
			errorMsg:    "failed to get grants",
			setupMock: func() {
				// Mock the AuthzKeeper call to return an error for invalid address
				mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("decoding bech32 failed: invalid bech32 string"))
			},
		},
		{
			name: "valid request with valid granter address",
			req: &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: "cosmos1jmjfq0tplp9tmx4v9uemw72y4d2wa5nr3xn9d3",
				MessageTypeUrl: "/cosmos.bank.v1beta1.MsgSend",
			},
			expectError: false,
			setupMock: func() {
				// Mock the AuthzKeeper call to return empty grants
				mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupMock()
			response, err := keeper.GranteesByMessageType(ctx, tt.req)

			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errorMsg)
				require.Nil(t, response)
			} else {
				require.NoError(t, err)
				require.NotNil(t, response)
				require.NotNil(t, response.Grantees)
				// For now, we expect empty results since this is a placeholder implementation
				require.Equal(t, 0, len(response.Grantees))
			}
		})
	}
}

func TestGranteesByMessageTypeQueryWithValidMessageTypes(t *testing.T) {
	keeper, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	validMessageTypes := []string{
		"/cosmos.bank.v1beta1.MsgSend",
		"/cosmos.staking.v1beta1.MsgDelegate",
		"/inference.inference.MsgClaimRewards",
		"/inference.inference.MsgSubmitSeed",
	}

	validGranterAddress := "cosmos1jmjfq0tplp9tmx4v9uemw72y4d2wa5nr3xn9d3"

	for _, msgType := range validMessageTypes {
		t.Run("message_type_"+msgType, func(t *testing.T) {
			// Set up mock expectation for each test case
			mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil)

			req := &types.QueryGranteesByMessageTypeRequest{
				GranterAddress: validGranterAddress,
				MessageTypeUrl: msgType,
			}

			response, err := keeper.GranteesByMessageType(ctx, req)

			require.NoError(t, err)
			require.NotNil(t, response)
			require.NotNil(t, response.Grantees)
			// For now, we expect empty results since this is a placeholder implementation
			require.Equal(t, 0, len(response.Grantees))
		})
	}
}

func TestGranteesByMessageTypeQuery_PaginatesAllPages(t *testing.T) {
	keeper, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	req := &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: "cosmos1jmjfq0tplp9tmx4v9uemw72y4d2wa5nr3xn9d3",
		MessageTypeUrl: "/inference.bls.MsgSubmitDealerPart",
	}

	gomock.InOrder(
		mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(
			&authztypes.QueryGranterGrantsResponse{
				Grants: []*authztypes.GrantAuthorization{},
				Pagination: &query.PageResponse{
					NextKey: []byte("next-page"),
				},
			},
			nil,
		),
		mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(
			&authztypes.QueryGranterGrantsResponse{
				Grants:     []*authztypes.GrantAuthorization{},
				Pagination: &query.PageResponse{},
			},
			nil,
		),
	)

	response, err := keeper.GranteesByMessageType(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Empty(t, response.Grantees)
}

func TestGranteesByMessageTypeQuery_LegacyWarmKeyMarkerAliasesClaimRewards(t *testing.T) {
	keeper, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	granter := sdk.AccAddress(bytes.Repeat([]byte{1}, 20))
	grantee := sdk.AccAddress(bytes.Repeat([]byte{2}, 20))
	authorization, err := codectypes.NewAnyWithValue(
		authztypes.NewGenericAuthorization(types.WarmKeyGrantMarkerTypeURL),
	)
	require.NoError(t, err)

	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(
		&authztypes.QueryGranterGrantsResponse{
			Grants: []*authztypes.GrantAuthorization{{
				Granter:       granter.String(),
				Grantee:       grantee.String(),
				Authorization: authorization,
			}},
		},
		nil,
	)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), grantee).Return(
		authtypes.NewBaseAccountWithAddress(grantee),
	)

	response, err := keeper.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: granter.String(),
		MessageTypeUrl: types.LegacyMsgStartInferenceTypeURL,
	})
	require.NoError(t, err)
	require.Len(t, response.Grantees, 1)
	require.Equal(t, grantee.String(), response.Grantees[0].Address)
}

// TestGranteesByMessageTypeQuery_CapsUnboundedGrantSet checks the query bounds
// its result at maxGranteesByMessageType (10000) instead of scanning the full,
// caller-controllable grant set.
func TestGranteesByMessageTypeQuery_CapsUnboundedGrantSet(t *testing.T) {
	keeper, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	const cap = 10000
	const overflow = cap + 5

	msgType := "/inference.bls.MsgSubmitDealerPart"
	granter := sdk.AccAddress(bytes.Repeat([]byte{9}, 20))
	authorization, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization(msgType))
	require.NoError(t, err)

	grants := make([]*authztypes.GrantAuthorization, 0, overflow)
	for i := 0; i < overflow; i++ {
		grantee := sdk.AccAddress([]byte(fmt.Sprintf("grantee-%012d", i)))
		grants = append(grants, &authztypes.GrantAuthorization{
			Granter:       granter.String(),
			Grantee:       grantee.String(),
			Authorization: authorization,
		})
	}

	// A single page carrying more matching grants than the cap; the loop must
	// stop at the cap without paging further.
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(
		&authztypes.QueryGranterGrantsResponse{Grants: grants},
		nil,
	).Times(1)
	// The query only reads account.GetPubKey() (nil here), so any base account
	// suffices; the grantee address comes from the grant, not the account.
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(
		authtypes.NewBaseAccountWithAddress(granter),
	).AnyTimes()

	response, err := keeper.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: granter.String(),
		MessageTypeUrl: msgType,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Grantees, cap, "grantee set must be bounded by maxGranteesByMessageType")
}
