package keeper

import (
	"context"
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
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/genesistransfer/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

// Mock implementations for testing
type mockAccountKeeper struct{}

func (m mockAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	return nil
}

func (m mockAccountKeeper) SetAccount(ctx context.Context, acc sdk.AccountI) {}

func (m mockAccountKeeper) NewAccountWithAddress(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(addr)
}

func (m mockAccountKeeper) NewPeriodicVestingAccount(baseAcc *vestingtypes.BaseVestingAccount, periods []vestingtypes.Period) *vestingtypes.PeriodicVestingAccount {
	return &vestingtypes.PeriodicVestingAccount{
		BaseVestingAccount: baseAcc,
		VestingPeriods:     periods,
	}
}

func (m mockAccountKeeper) NewContinuousVestingAccount(baseAcc *vestingtypes.BaseVestingAccount, startTime int64, endTime int64) *vestingtypes.ContinuousVestingAccount {
	return &vestingtypes.ContinuousVestingAccount{
		BaseVestingAccount: baseAcc,
		StartTime:          startTime,
	}
}

func (m mockAccountKeeper) NewDelayedVestingAccount(baseAcc *vestingtypes.BaseVestingAccount, endTime int64) *vestingtypes.DelayedVestingAccount {
	return &vestingtypes.DelayedVestingAccount{
		BaseVestingAccount: baseAcc,
	}
}

type mockBankKeeper struct{}

func (m mockBankKeeper) SpendableCoins(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	return sdk.NewCoins()
}

func (m mockBankKeeper) GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	return sdk.NewCoins()
}

type mockBookkeepingBankKeeper struct{}

func (m mockBookkeepingBankKeeper) SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	return nil
}

func (m mockBookkeepingBankKeeper) SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
	return nil
}

func (m mockBookkeepingBankKeeper) SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	return nil
}

func GenesistransferKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
	// Configure SDK with proper Bech32 prefix for gonka
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

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
		mockAccountKeeper{},
		mockBankKeeper{},
		mockBookkeepingBankKeeper{},
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}
