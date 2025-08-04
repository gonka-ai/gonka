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

	"github.com/productscience/inference/x/collateral/keeper"
	"github.com/productscience/inference/x/collateral/types"
)

// CollateralMocks holds all the mock keepers for testing
type CollateralMocks struct {
	BankKeeper *MockBookkeepingBankKeeper
}

func CollateralKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
	ctrl := gomock.NewController(t)
	bankKeeper := NewMockBookkeepingBankKeeper(ctrl)
	// StakingKeeper and InferenceKeeper can be nil for basic tests
	k, ctx := CollateralKeeperWithMock(t, bankKeeper)

	return k, ctx
}

func CollateralKeeperReturningMocks(t testing.TB) (keeper.Keeper, sdk.Context, CollateralMocks) {
	ctrl := gomock.NewController(t)
	bankKeeper := NewMockBookkeepingBankKeeper(ctrl)

	k, ctx := CollateralKeeperWithMock(t, bankKeeper)

	mocks := CollateralMocks{
		BankKeeper: bankKeeper,
	}

	return k, ctx, mocks
}

func CollateralKeeperWithMock(
	t testing.TB,
	bankKeeper *MockBookkeepingBankKeeper,
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
		nil,
		bankKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}
