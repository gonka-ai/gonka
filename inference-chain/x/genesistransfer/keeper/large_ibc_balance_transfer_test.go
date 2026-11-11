package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	"github.com/stretchr/testify/require"

	bookkeeperkeeper "github.com/productscience/inference/x/bookkeeper/keeper"
	bookkeepertypes "github.com/productscience/inference/x/bookkeeper/types"
	"github.com/productscience/inference/x/genesistransfer/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

type realTransferStack struct {
	ctx        sdk.Context
	account    authkeeper.AccountKeeper
	bank       bankkeeper.BaseKeeper
	keeper     keeper.Keeper
	msgServer  types.MsgServer
	moduleAddr sdk.AccAddress
}

func newRealTransferStack(t *testing.T, cfg bookkeeperkeeper.LogConfig, blockTime time.Time) realTransferStack {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	authKey := storetypes.NewKVStoreKey(authtypes.StoreKey)
	bankKey := storetypes.NewKVStoreKey(banktypes.StoreKey)
	bookKey := storetypes.NewKVStoreKey(bookkeepertypes.StoreKey)
	gtKey := storetypes.NewKVStoreKey(types.StoreKey)

	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	for _, key := range []*storetypes.KVStoreKey{authKey, bankKey, bookKey, gtKey} {
		ms.MountStoreWithDB(key, storetypes.StoreTypeIAVL, db)
	}
	require.NoError(t, ms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	vestingtypes.RegisterInterfaces(registry)
	banktypes.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	authority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	maccPerms := map[string][]string{
		minttypes.ModuleName: {authtypes.Minter},
		types.ModuleName:     {authtypes.Minter, authtypes.Burner},
	}

	ak := authkeeper.NewAccountKeeper(cdc, runtime.NewKVStoreService(authKey), authtypes.ProtoBaseAccount, maccPerms, addresscodec.NewBech32Codec("gonka"), "gonka", authority)
	bk := bankkeeper.NewBaseKeeper(cdc, runtime.NewKVStoreService(bankKey), ak, map[string]bool{}, authority, log.NewNopLogger())
	book := bookkeeperkeeper.NewKeeper(cdc, runtime.NewKVStoreService(bookKey), log.NewNopLogger(), authority, bk, cfg)
	gk := keeper.NewKeeper(cdc, runtime.NewKVStoreService(gtKey), log.NewNopLogger(), authority, ak, bk, book)

	ctx := sdk.NewContext(ms, cmtproto.Header{Height: 100, Time: blockTime}, false, log.NewNopLogger())

	return realTransferStack{
		ctx:        ctx,
		account:    ak,
		bank:       bk,
		keeper:     gk,
		msgServer:  keeper.NewMsgServerImpl(gk),
		moduleAddr: authtypes.NewModuleAddress(types.ModuleName),
	}
}

func (s realTransferStack) fund(t *testing.T, addr sdk.AccAddress, coins sdk.Coins) {
	require.NoError(t, s.bank.MintCoins(s.ctx, minttypes.ModuleName, coins))
	require.NoError(t, s.bank.SendCoinsFromModuleToAccount(s.ctx, minttypes.ModuleName, addr, coins))
}

func (s realTransferStack) deliver(msg *types.MsgTransferOwnership) (recovered any, err error) {
	cacheCtx, write := s.ctx.CacheContext()
	defer func() {
		if r := recover(); r != nil {
			recovered = r
		}
	}()
	_, err = s.msgServer.TransferOwnership(cacheCtx, msg)
	if err == nil {
		write()
	}
	return nil, err
}

func TestTransferOwnershipWithIBCBalanceAboveInt64(t *testing.T) {
	configs := map[string]bookkeeperkeeper.LogConfig{
		"all_logging": {DoubleEntry: true, SimpleEntry: true, LogLevel: "info"},
		"logging_off": {LogLevel: "info"},
	}

	for name, cfg := range configs {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0).UTC()
			s := newRealTransferStack(t, cfg, now)

			genesisAddr := sdk.AccAddress([]byte("genesis_account_____"))
			recipientAddr := sdk.AccAddress([]byte("recipient_account___"))
			ibcDenom := "ibc/C679139C206D0E7379C1526A19B91DD18A9B3C1A28E76C099962629894E4D193"
			bigAmount := math.NewIntFromUint64(1 << 63)
			vested := sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000_000)))
			voucher := sdk.NewCoins(sdk.NewCoin(ibcDenom, bigAmount))

			base, ok := s.account.NewAccountWithAddress(s.ctx, genesisAddr).(*authtypes.BaseAccount)
			require.True(t, ok)
			vestingAcc, err := vestingtypes.NewContinuousVestingAccount(base, vested, now.Add(-time.Hour).Unix(), now.Add(365*24*time.Hour).Unix())
			require.NoError(t, err)
			s.account.SetAccount(s.ctx, vestingAcc)
			s.fund(t, genesisAddr, vested)
			s.fund(t, genesisAddr, voucher)

			require.NoError(t, s.keeper.SetParams(s.ctx, types.NewParams([]string{genesisAddr.String()}, true)))

			before := s.bank.GetAllBalances(s.ctx, genesisAddr)
			require.Equal(t, bigAmount, before.AmountOf(ibcDenom))

			recovered, err := s.deliver(&types.MsgTransferOwnership{
				GenesisAddress:   genesisAddr.String(),
				RecipientAddress: recipientAddr.String(),
			})
			require.Nil(t, recovered)
			require.NoError(t, err)

			require.True(t, s.bank.GetAllBalances(s.ctx, genesisAddr).IsZero())
			require.True(t, s.bank.GetAllBalances(s.ctx, s.moduleAddr).IsZero())

			after := s.bank.GetAllBalances(s.ctx, recipientAddr)
			require.Equal(t, before, after)
			require.Equal(t, bigAmount, s.bank.SpendableCoins(s.ctx, recipientAddr).AmountOf(ibcDenom))

			recipientAcc := s.account.GetAccount(s.ctx, recipientAddr)
			recipientVesting, ok := recipientAcc.(*vestingtypes.ContinuousVestingAccount)
			require.True(t, ok)
			require.Equal(t, now.Add(365*24*time.Hour).Unix(), recipientVesting.EndTime)
			require.True(t, recipientVesting.OriginalVesting.AmountOf("ngonka").IsPositive())
			require.True(t, recipientVesting.OriginalVesting.AmountOf(ibcDenom).IsZero())

			record, found, err := s.keeper.GetTransferRecord(s.ctx, genesisAddr)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, record.Completed)
			require.Equal(t, recipientAddr.String(), record.RecipientAddress)
			require.Equal(t, uint64(100), record.TransferHeight)
			require.ElementsMatch(t, []string{"ngonka", ibcDenom}, record.TransferredDenoms)

			var completed int
			for _, ev := range s.ctx.EventManager().Events() {
				if ev.Type != "ownership_transfer_completed" {
					continue
				}
				for _, attr := range ev.Attributes {
					if attr.Key == "genesis_address" && attr.Value == genesisAddr.String() {
						completed++
					}
				}
			}
			require.Equal(t, 1, completed)

			_, err = s.deliver(&types.MsgTransferOwnership{
				GenesisAddress:   genesisAddr.String(),
				RecipientAddress: recipientAddr.String(),
			})
			require.Error(t, err)
		})
	}
}
