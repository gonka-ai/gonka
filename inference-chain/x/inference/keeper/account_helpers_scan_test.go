package keeper_test

import (
	"fmt"
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	keepertest "github.com/productscience/inference/testutil/keeper"
)

// TestGetAccountPubKeysWithGrantees_BoundsScan checks the sibling helper bounds
// its grant scan the same way GranteesByMessageType does: a granter with a huge
// grant set must not make the query walk the whole set (Limit 0 would set
// CountTotal and scan the full prefix; an uncapped loop would page to the end).
func TestGetAccountPubKeysWithGrantees_BoundsScan(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	granter := sdk.AccAddress([]byte("granter-000000000000"))
	acc := authtypes.NewBaseAccount(granter, secp256k1.GenPrivKey().PubKey(), 0, 0)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(acc).AnyTimes()

	auth, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"))
	require.NoError(t, err)
	const pages, perPage = 200, 100
	calls := 0
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ interface{}, _ *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			p := calls
			calls++
			gs := make([]*authztypes.GrantAuthorization, 0, perPage)
			for i := 0; i < perPage; i++ {
				grantee := sdk.AccAddress([]byte(fmt.Sprintf("grantee-%012d", p*perPage+i)))
				gs = append(gs, &authztypes.GrantAuthorization{Granter: granter.String(), Grantee: grantee.String(), Authorization: auth})
			}
			var next []byte
			if p+1 < pages {
				next = []byte(fmt.Sprintf("page-%d", p+1))
			}
			return &authztypes.QueryGranterGrantsResponse{Grants: gs, Pagination: &query.PageResponse{NextKey: next}}, nil
		}).AnyTimes()

	ms := keeper.NewMsgServerImpl(k)
	_, err = keeper.GetAccountPubKeysWithGranteesForTest(ms, ctx, granter.String())
	require.NoError(t, err)
	require.LessOrEqual(t, calls*perPage, 10000, "grant scan must be bounded by the cap")
}
