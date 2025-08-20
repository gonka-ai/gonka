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
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/restrictions/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

// SimpleAccountKeeper is a simple mock for testing
type SimpleAccountKeeper struct{}

func (m *SimpleAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	// For testing purposes, simulate what the real AccountKeeper would return
	// Module accounts are created by modules during genesis/runtime, so we simulate this
	addrStr := addr.String()

	// Check if this address matches any module account address
	knownModules := []string{
		"fee_collector", "distribution", "mint", "bonded_tokens_pool", "not_bonded_tokens_pool", "gov",
		"inference", "streamvesting", "collateral", "bookkeeper", "bls", "genesistransfer", "restrictions",
		"top_reward", "pre_programmed_sale", // Special accounts
	}

	for _, moduleName := range knownModules {
		moduleAddr := authtypes.NewModuleAddress(moduleName)
		if addr.Equals(moduleAddr) {
			// Return a mock module account (simulating what real modules would create)
			return &authtypes.ModuleAccount{
				BaseAccount: &authtypes.BaseAccount{Address: addrStr},
				Name:        moduleName,
			}
		}
	}

	// Return a regular account for non-module addresses
	return &authtypes.BaseAccount{Address: addrStr}
}

// SimpleBankKeeper is a simple mock for testing
type SimpleBankKeeper struct{}

func (m *SimpleBankKeeper) SpendableCoins(ctx context.Context, addr sdk.AccAddress) sdk.Coins {
	return sdk.NewCoins()
}

func (m *SimpleBankKeeper) SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error {
	return nil
}

func RestrictionsKeeper(t testing.TB) (keeper.Keeper, sdk.Context) {
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
		&SimpleAccountKeeper{},
		&SimpleBankKeeper{},
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	// Initialize params
	if err := k.SetParams(ctx, types.DefaultParams()); err != nil {
		panic(err)
	}

	return k, ctx
}
