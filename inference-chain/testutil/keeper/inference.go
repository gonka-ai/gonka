package keeper

import (
	"go.uber.org/mock/gomock"
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

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func InferenceKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
	ctrl := gomock.NewController(t)
	escrowKeeper := NewMockBankEscrowKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSetMock := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageServer(ctrl)
	mock, context := InferenceKeeperWithMock(t, escrowKeeper, accountKeeperMock, validatorSetMock, groupMock)
	escrowKeeper.ExpectAny(context)
	return mock, context
}

type InferenceMocks struct {
	BankKeeper    *MockBankEscrowKeeper
	AccountKeeper *MockAccountKeeper
}

func InferenceKeeperReturningMock(t testing.TB) (keeper.Keeper, sdk.Context, InferenceMocks) {
	ctrl := gomock.NewController(t)
	escrowKeeper := NewMockBankEscrowKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSet := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageServer(ctrl)
	keep, context := InferenceKeeperWithMock(t, escrowKeeper, accountKeeperMock, validatorSet, groupMock)
	mocks := InferenceMocks{
		BankKeeper:    escrowKeeper,
		AccountKeeper: accountKeeperMock,
	}
	return keep, context, mocks
}

func InferenceKeeperWithMock(
	t testing.TB,
	bankMock *MockBankEscrowKeeper,
	accountKeeper types.AccountKeeper,
	validatorSet types.ValidatorSet,
	groupMock types.GroupMessageServer,
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
		bankMock,
		nil,
		groupMock,
		validatorSet,
		nil,
		accountKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}
