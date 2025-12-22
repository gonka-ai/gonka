package keeper

import (
	"context"
	"fmt"
	"testing"
	"time"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/slog"

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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

func InferenceKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
	ctrl := gomock.NewController(t)
	bankKeeper := NewMockBookkeepingBankKeeper(ctrl)
	bankViewKeeper := NewMockBankKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSetMock := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageKeeper(ctrl)
	stakingMock := NewMockStakingKeeper(ctrl)
	collateralMock := NewMockCollateralKeeper(ctrl)
	streamvestingMock := NewMockStreamVestingKeeper(ctrl)
	authzKeeper := NewMockAuthzKeeper(ctrl)
	upgradeKeeper := NewMockUpgradeKeeper(ctrl)
	mock, context := InferenceKeeperWithMock(t, bankKeeper, accountKeeperMock, validatorSetMock, groupMock, stakingMock, collateralMock, streamvestingMock, bankViewKeeper, authzKeeper, upgradeKeeper)
	bankKeeper.ExpectAny(context)
	return mock, context
}

// InferenceKeeperWithUpgradeKeeper creates a test keeper with a specific UpgradeKeeper mock
func InferenceKeeperWithUpgradeKeeper(t testing.TB, upgradeKeeper types.UpgradeKeeper) (keeper.Keeper, sdk.Context) {
	ctrl := gomock.NewController(t)
	bankKeeper := NewMockBookkeepingBankKeeper(ctrl)
	bankViewKeeper := NewMockBankKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSetMock := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageKeeper(ctrl)
	stakingMock := NewMockStakingKeeper(ctrl)
	collateralMock := NewMockCollateralKeeper(ctrl)
	streamvestingMock := NewMockStreamVestingKeeper(ctrl)
	authzKeeper := NewMockAuthzKeeper(ctrl)
	mock, context := InferenceKeeperWithMock(t, bankKeeper, accountKeeperMock, validatorSetMock, groupMock, stakingMock, collateralMock, streamvestingMock, bankViewKeeper, authzKeeper, upgradeKeeper)
	bankKeeper.ExpectAny(context)
	return mock, context
}

type InferenceMocks struct {
	BankKeeper          *MockBookkeepingBankKeeper
	AccountKeeper       *MockAccountKeeper
	GroupKeeper         *MockGroupMessageKeeper
	StakingKeeper       *MockStakingKeeper
	CollateralKeeper    *MockCollateralKeeper
	StreamVestingKeeper *MockStreamVestingKeeper
	BankViewKeeper      *MockBankKeeper
	AuthzKeeper         *MockAuthzKeeper
}

func (mocks *InferenceMocks) StubForInitGenesis(ctx context.Context) {
	// Enable duplicate denom registration tolerance for tests that call InitGenesis
	inference.IgnoreDuplicateDenomRegistration = true
	mocks.StubForInitGenesisWithValidators(ctx, []stakingtypes.Validator{})
}

func (mocks *InferenceMocks) StubForInitGenesisWithValidators(ctx context.Context, validators []stakingtypes.Validator) {
	mocks.AccountKeeper.EXPECT().GetModuleAccount(ctx, types.TopRewardPoolAccName)
	mocks.AccountKeeper.EXPECT().GetModuleAccount(ctx, types.PreProgrammedSaleAccName)
	mocks.AccountKeeper.EXPECT().GetModuleAccount(ctx, types.BridgeEscrowAccName)
	// Kind of pointless to test the exact amount of coins minted, it'd just be a repeat of the code
	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.TopRewardPoolAccName, gomock.Any(), gomock.Any())
	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.PreProgrammedSaleAccName, gomock.Any(), gomock.Any())
	mocks.BankViewKeeper.EXPECT().GetDenomMetaData(ctx, types.BaseCoin).Return(banktypes.Metadata{
		Base: types.BaseCoin,
		DenomUnits: []*banktypes.DenomUnit{
			{
				Denom:    types.BaseCoin,
				Exponent: 0,
			},
			{
				Denom:    types.NativeCoin,
				Exponent: 9,
			},
		},
	}, true)

	mocks.ExpectCreateGroupWithPolicyCall(ctx, 1)
	// Actually can just return any as well
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(ctx, gomock.Any()).Return(&group.MsgUpdateGroupMetadataResponse{}, nil).
		AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMembersResponse{}, nil).
		AnyTimes()

	mocks.StakingKeeper.EXPECT().GetAllValidators(ctx).Return(validators, nil).
		Times(1)
}

func (mocks *InferenceMocks) ExpectCreateGroupWithPolicyCall(ctx context.Context, groupId uint64) {
	mocks.GroupKeeper.EXPECT().CreateGroupWithPolicy(ctx, gomock.Any()).Return(&group.MsgCreateGroupWithPolicyResponse{
		GroupId:            groupId,
		GroupPolicyAddress: fmt.Sprintf("group-policy-address-%d", groupId),
	}, nil).Times(1)
}

func (mocks *InferenceMocks) ExpectAnyCreateGroupWithPolicyCall() *gomock.Call {
	return mocks.GroupKeeper.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(&group.MsgCreateGroupWithPolicyResponse{
		GroupId:            0,
		GroupPolicyAddress: "group-policy-address",
	}, nil).Times(1)
}

func (mocks *InferenceMocks) StubGenesisState() types.GenesisState {
	return types.GenesisState{
		Params:            types.DefaultParams(),
		GenesisOnlyParams: types.DefaultGenesisOnlyParams(),
		ModelList:         GenesisModelsTestList(),
	}
}

func InferenceKeeperReturningMocks(t testing.TB) (keeper.Keeper, sdk.Context, InferenceMocks) {
	ctrl := gomock.NewController(t)
	bankKeeper := NewMockBookkeepingBankKeeper(ctrl)
	bankViewKeeper := NewMockBankKeeper(ctrl)
	accountKeeperMock := NewMockAccountKeeper(ctrl)
	validatorSet := NewMockValidatorSet(ctrl)
	groupMock := NewMockGroupMessageKeeper(ctrl)
	stakingMock := NewMockStakingKeeper(ctrl)
	collateralMock := NewMockCollateralKeeper(ctrl)
	streamvestingMock := NewMockStreamVestingKeeper(ctrl)
	authzKeeper := NewMockAuthzKeeper(ctrl)
	upgradeKeeper := NewMockUpgradeKeeper(ctrl)
	keep, context := InferenceKeeperWithMock(t, bankKeeper, accountKeeperMock, validatorSet, groupMock, stakingMock, collateralMock, streamvestingMock, bankViewKeeper, authzKeeper, upgradeKeeper)
	keep.SetTokenomicsData(context, types.TokenomicsData{})
	genesisParams := types.DefaultGenesisOnlyParams()
	keep.SetGenesisOnlyParams(context, &genesisParams)
	mocks := InferenceMocks{
		BankKeeper:          bankKeeper,
		AccountKeeper:       accountKeeperMock,
		GroupKeeper:         groupMock,
		StakingKeeper:       stakingMock,
		CollateralKeeper:    collateralMock,
		StreamVestingKeeper: streamvestingMock,
		BankViewKeeper:      bankViewKeeper,
		AuthzKeeper:         authzKeeper,
	}
	return keep, context, mocks
}

func InferenceKeeperWithMock(
	t testing.TB,
	bankMock *MockBookkeepingBankKeeper,
	accountKeeper types.AccountKeeper,
	validatorSet types.ValidatorSet,
	groupMock types.GroupMessageKeeper,
	stakingKeeper types.StakingKeeper,
	collateralKeeper types.CollateralKeeper,
	streamvestingKeeper types.StreamVestingKeeper,
	bankViewMock *MockBankKeeper,
	authzKeeper types.AuthzKeeper,
	upgradeKeeper types.UpgradeKeeper,
) (keeper.Keeper, sdk.Context) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	blsStoreKey := storetypes.NewKVStoreKey(blstypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	// Create BLS keeper for testing
	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		PrintlnLogger{},
		authority.String(),
	)

	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		PrintlnLogger{},
		authority.String(),
		bankMock,
		bankViewMock,
		groupMock,
		validatorSet,
		stakingKeeper,
		accountKeeper,
		blsKeeper,
		collateralKeeper,
		streamvestingKeeper,
		authzKeeper,
		nil,
		upgradeKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger()).
		WithBlockTime(time.Now()).
		WithHeaderInfo(header.Info{
			Hash: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		})

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	// Initialize BLS params
	if err := blsKeeper.SetParams(ctx, blstypes.DefaultParams()); err != nil {
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
