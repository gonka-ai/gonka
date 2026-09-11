package pocchallenge

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/header"
	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*Store, sdk.Context) {
	t.Helper()
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	storeKey := storetypes.NewKVStoreKey("pocchallenge")
	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger()).
		WithBlockTime(time.Now()).
		WithHeaderInfo(header.Info{Hash: make([]byte, 32)})

	sb := collections.NewSchemaBuilder(runtime.NewKVStoreService(storeKey))
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	s := NewStore(sb, cdc)
	_, err := sb.Build()
	require.NoError(t, err)
	return s, ctx
}
