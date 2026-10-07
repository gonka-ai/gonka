package app_test

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
)

func ibcHash(a *app.App) []byte {
	return a.CommitMultiStore().GetCommitKVStore(a.GetKey(ibcexported.StoreKey)).LastCommitID().Hash
}

func localhostHeight(t *testing.T, a *app.App) uint64 {
	ctx := a.BaseApp.NewUncachedContext(false, cmtproto.Header{})
	cs, ok := a.GetIBCKeeper().ClientKeeper.GetClientState(ctx, ibcexported.LocalhostClientID)
	require.True(t, ok)
	return cs.GetLatestHeight().GetRevisionHeight()
}

func commitEmptyBlock(t *testing.T, a *app.App, h int64) {
	_, err := a.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h})
	require.NoError(t, err)
	_, err = a.Commit()
	require.NoError(t, err)
}

func TestLocalhostClientWriteStopsWhenNotAllowed(t *testing.T) {
	a := createTestApp(t)

	// Default params (allowed_clients ["*"], as on mainnet): every empty block rewrites the ibc store.
	prev := ibcHash(a)
	for h := int64(2); h <= 4; h++ {
		commitEmptyBlock(t, a, h)
		cur := ibcHash(a)
		require.NotEqual(t, prev, cur, "ibc store unchanged at %d", h)
		prev = cur
		require.Equal(t, uint64(h), localhostHeight(t, a))
	}

	// Allow only 07-tendermint: localhost becomes Unauthorized, BeginBlocker skips the write.
	ctx := a.BaseApp.NewUncachedContext(false, cmtproto.Header{Height: 5})
	a.GetIBCKeeper().ClientKeeper.SetParams(ctx, clienttypes.NewParams(ibcexported.Tendermint))
	commitEmptyBlock(t, a, 5) // commits the params change
	prev = ibcHash(a)
	for h := int64(6); h <= 8; h++ {
		commitEmptyBlock(t, a, h)
		require.Equal(t, prev, ibcHash(a), "ibc store changed at %d", h)
	}
	require.Equal(t, uint64(4), localhostHeight(t, a))
	require.True(t, a.GetIBCKeeper().ClientKeeper.GetParams(ctx).IsAllowedClient(ibcexported.Tendermint))
}
