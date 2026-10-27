package keeper_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestQueryBridgeKeyLink(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	gonkaPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address())
	account := authtypes.NewBaseAccount(creator, gonkaPriv.PubKey(), 1, 0)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creator).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), creator).Return(account).AnyTimes()

	owner := sdk.AccAddress(ethPriv.PubKey().Address()).String()
	missing, err := k.BridgeKeyLink(ctx, &types.QueryBridgeKeyLinkRequest{OwnerAddress: owner})
	require.NoError(t, err)
	require.False(t, missing.Found)
	require.Equal(t, owner, missing.OwnerAddress)
	require.Empty(t, missing.RecipientAddress)
	require.Zero(t, missing.Nonce)

	_, err = ms.RegisterBridgeKeyLink(ctx, bridgeKeyLinkMsg(t, ctx.ChainID(), gonkaPriv, ethPriv, 1))
	require.NoError(t, err)

	found, err := k.BridgeKeyLink(ctx, &types.QueryBridgeKeyLinkRequest{OwnerAddress: owner})
	require.NoError(t, err)
	require.True(t, found.Found)
	require.Equal(t, owner, found.OwnerAddress)
	require.Equal(t, creator.String(), found.RecipientAddress)
	require.Equal(t, uint64(1), found.Nonce)

	_, err = k.BridgeKeyLink(ctx, &types.QueryBridgeKeyLinkRequest{OwnerAddress: "not-an-address"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = k.BridgeKeyLink(ctx, &types.QueryBridgeKeyLinkRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
