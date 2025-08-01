package keeper

import (
	"testing"

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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/streamvesting/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

// StreamVestingMocks holds all the mock keepers for testing
type StreamVestingMocks struct {
	BankKeeper *MockBookkeepingBankKeeper
}

func StreamvestingKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
	ctrl := gomock.NewController(t)
	bankEscrowKeeper := NewMockBookkeepingBankKeeper(ctrl)
	// BankEscrowKeeper can be nil for basic tests
	k, ctx := StreamVestingKeeperWithMock(t, bankEscrowKeeper)

	return k, ctx
}

func StreamVestingKeeperWithMocks(t testing.TB) (keeper.Keeper, sdk.Context, StreamVestingMocks) {
	ctrl := gomock.NewController(t)
	bankEscrowKeeper := NewMockBookkeepingBankKeeper(ctrl)

	k, ctx := StreamVestingKeeperWithMock(t, bankEscrowKeeper)

	mocks := StreamVestingMocks{
		BankKeeper: bankEscrowKeeper,
	}

	return k, ctx, mocks
}

func StreamVestingKeeperWithMock(
	t testing.TB,
	bankEscrowKeeper *MockBookkeepingBankKeeper,
) (keeper.Keeper, sdk.Context) {
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		log.NewNopLogger(),
		authority.String(),
		nil, // BankKeeper can be nil for tests
		bankEscrowKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}
