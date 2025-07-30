package keeper

import (
	"context"
	"fmt"
	"testing"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/slog"

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
	inference "github.com/productscience/inference/x/inference/module"
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

func (mocks *InferenceMocks) StubForInitGenesis(ctx context.Context) {
	// Enable duplicate denom registration tolerance for tests that call InitGenesis
	inference.IgnoreDuplicateDenomRegistration = true
	mocks.StubForInitGenesisWithValidators(ctx, []stakingtypes.Validator{})
}

func (mocks *InferenceMocks) StubForInitGenesisWithValidators(ctx context.Context, validators []stakingtypes.Validator) {
	mocks.AccountKeeper.EXPECT().GetModuleAccount(ctx, types.TopRewardPoolAccName)
	mocks.AccountKeeper.EXPECT().GetModuleAccount(ctx, types.PreProgrammedSaleAccName)
	// Kind of pointless to test the exact amount of coins minted, it'd just be a repeat of the code
	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.TopRewardPoolAccName, gomock.Any())
	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.PreProgrammedSaleAccName, gomock.Any())
	mocks.BankKeeper.EXPECT().GetDenomMetaData(ctx, types.BaseCoin).Return(banktypes.Metadata{
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
		nil,
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
