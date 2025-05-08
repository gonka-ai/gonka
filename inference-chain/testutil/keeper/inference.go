package keeper

import (
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/slog"
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
	groupMock := NewMockGroupMessageKeeper(ctrl)
	stakingMock := NewMockStakingKeeper(ctrl)
	mock, context := InferenceKeeperWithMock(t, escrowKeeper, accountKeeperMock, validatorSetMock, groupMock, stakingMock)
	escrowKeeper.ExpectAny(context)
	return mock, context
}

type InferenceMocks struct {
	BankKeeper    *MockBankEscrowKeeper
	AccountKeeper *MockAccountKeeper
	GroupKeeper   *MockGroupMessageKeeper
	StakingKeeper *MockStakingKeeper
}

func InferenceKeeperReturningMocks(t testing.TB) (keeper.Keeper, sdk.Context, InferenceMocks) {
	ctrl := gomock.NewController(t)
	escrowKeeper := NewMockBankEscrowKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSet := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageKeeper(ctrl)
	stakingMock := NewMockStakingKeeper(ctrl)
	keep, context := InferenceKeeperWithMock(t, escrowKeeper, accountKeeperMock, validatorSet, groupMock, stakingMock)
	keep.SetTokenomicsData(context, types.TokenomicsData{})
	genesisParams := types.DefaultGenesisOnlyParams()
	keep.SetGenesisOnlyParams(context, &genesisParams)
	mocks := InferenceMocks{
		BankKeeper:    escrowKeeper,
		AccountKeeper: accountKeeperMock,
		GroupKeeper:   groupMock,
		StakingKeeper: stakingMock,
	}
	return keep, context, mocks
}

func InferenceKeeperWithMock(
	t testing.TB,
	bankMock *MockBankEscrowKeeper,
	accountKeeper types.AccountKeeper,
	validatorSet types.ValidatorSet,
	groupMock types.GroupMessageKeeper,
	stakingKeeper types.StakingKeeper,
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
		PrintlnLogger{},
		authority.String(),
		bankMock,
		nil,
		groupMock,
		validatorSet,
		stakingKeeper,
		accountKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}

type PrintlnLogger struct{}

func (PrintlnLogger) Info(msg string, keyVals ...any) {
	slog.Info(msg, keyVals...)
}

func (PrintlnLogger) Warn(msg string, keyVals ...any) {
	slog.Warn(msg, keyVals...)
}

func (PrintlnLogger) Error(msg string, keyVals ...any) {
	slog.Error(msg, keyVals...)
}

func (PrintlnLogger) Debug(msg string, keyVals ...any) {
	slog.Debug(msg, keyVals...)
}

func (PrintlnLogger) With(keyVals ...any) log.Logger {
	return PrintlnLogger{}
}

func (PrintlnLogger) Impl() any {
	return nil
}
