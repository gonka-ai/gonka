package keeper_test

import (
	"context"
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

	"github.com/productscience/inference/x/genesistransfer/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

type memoBank struct{ bankkeeper.BaseKeeper }

func (b memoBank) SendCoins(ctx context.Context, f, t sdk.AccAddress, a sdk.Coins, _ string) error {
	return b.BaseKeeper.SendCoins(ctx, f, t, a)
}
func (b memoBank) SendCoinsFromAccountToModule(ctx context.Context, f sdk.AccAddress, m string, a sdk.Coins, _ string) error {
	return b.BaseKeeper.SendCoinsFromAccountToModule(ctx, f, m, a)
}
func (b memoBank) SendCoinsFromModuleToAccount(ctx context.Context, m string, t sdk.AccAddress, a sdk.Coins, _ string) error {
	return b.BaseKeeper.SendCoinsFromModuleToAccount(ctx, m, t, a)
}

type vestingEnv struct {
	ctx sdk.Context
	ak  authkeeper.AccountKeeper
	bk  bankkeeper.BaseKeeper
	k   keeper.Keeper
	srv types.MsgServer
}

// newVestingEnv wires the real x/auth and x/bank keepers to the genesistransfer keeper.
func newVestingEnv(t *testing.T, now int64) vestingEnv {
	keys := storetypes.NewKVStoreKeys(types.StoreKey, authtypes.StoreKey, banktypes.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	for _, k := range keys {
		ms.MountStoreWithDB(k, storetypes.StoreTypeIAVL, db)
	}
	require.NoError(t, ms.LoadLatestVersion())

	reg := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(reg)
	vestingtypes.RegisterInterfaces(reg)
	cryptocodec.RegisterInterfaces(reg)
	cdc := codec.NewProtoCodec(reg)
	prefix := sdk.GetConfig().GetBech32AccountAddrPrefix()
	gov, err := addresscodec.NewBech32Codec(prefix).BytesToString(authtypes.NewModuleAddress(govtypes.ModuleName))
	require.NoError(t, err)

	ak := authkeeper.NewAccountKeeper(cdc, runtime.NewKVStoreService(keys[authtypes.StoreKey]), authtypes.ProtoBaseAccount,
		map[string][]string{types.ModuleName: nil, minttypes.ModuleName: {authtypes.Minter}},
		addresscodec.NewBech32Codec(prefix), prefix, gov)
	bk := bankkeeper.NewBaseKeeper(cdc, runtime.NewKVStoreService(keys[banktypes.StoreKey]), ak, map[string]bool{}, gov, log.NewNopLogger())
	k := keeper.NewKeeper(cdc, runtime.NewKVStoreService(keys[types.StoreKey]), log.NewNopLogger(), gov, ak, bk, memoBank{bk})
	ctx := sdk.NewContext(ms, cmtproto.Header{Height: 1, Time: time.Unix(now, 0)}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return vestingEnv{ctx: ctx, ak: ak, bk: bk, k: k, srv: keeper.NewMsgServerImpl(k)}
}

func (e vestingEnv) fundContinuous(t *testing.T, addr sdk.AccAddress, amount int64, start, end int64) {
	coins := sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(amount)))
	base := authtypes.NewBaseAccountWithAddress(addr)
	base.AccountNumber = e.ak.NextAccountNumber(e.ctx)
	acc, err := vestingtypes.NewContinuousVestingAccount(base, coins, start, end)
	require.NoError(t, err)
	e.ak.SetAccount(e.ctx, acc)
	require.NoError(t, e.bk.MintCoins(e.ctx, minttypes.ModuleName, coins))
	require.NoError(t, e.bk.SendCoinsFromModuleToAccount(e.ctx, minttypes.ModuleName, addr, coins))
}

func (e vestingEnv) locked(addr sdk.AccAddress) math.Int {
	return e.bk.GetBalance(e.ctx, addr, "ngonka").Amount.Sub(e.bk.SpendableCoins(e.ctx, addr).AmountOf("ngonka"))
}

// A recipient that is already vesting to the same end time keeps its own lock.
func TestTransferOwnership_VestingRecipientKeepsItsLock(t *testing.T) {
	start, now, end := int64(1_000_000), int64(1_400_000), int64(2_000_000)
	e := newVestingEnv(t, now)
	a := sdk.AccAddress([]byte("genesis-A-----------"))
	b := sdk.AccAddress([]byte("recipient-B---------"))
	e.fundContinuous(t, a, 1_000_000, start, end)
	e.fundContinuous(t, b, 50_000_000, start, end)
	lockedA, lockedB := e.locked(a), e.locked(b)

	_, err := e.srv.TransferOwnership(e.ctx, &types.MsgTransferOwnership{GenesisAddress: mustStr(t, a), RecipientAddress: mustStr(t, b)})
	require.NoError(t, err)

	require.Equal(t, lockedA.Add(lockedB), e.locked(b))
	acc, ok := e.ak.GetAccount(e.ctx, b).(*vestingtypes.ContinuousVestingAccount)
	require.True(t, ok)
	require.Equal(t, end, acc.EndTime)
	// Halfway to the end, half of the combined remaining lock is left.
	mid := e.ctx.WithBlockTime(time.Unix(now+(end-now)/2, 0))
	require.Equal(t, lockedA.Add(lockedB).QuoRaw(2), acc.GetVestingCoins(mid.BlockTime()).AmountOf("ngonka"))
}

// A schedule that cannot be combined is refused rather than replaced.
func TestTransferOwnership_RejectsUncombinableVestingRecipient(t *testing.T) {
	start, now, end := int64(1_000_000), int64(1_400_000), int64(2_000_000)
	e := newVestingEnv(t, now)
	a := sdk.AccAddress([]byte("genesis-A-----------"))
	b := sdk.AccAddress([]byte("recipient-B---------"))
	e.fundContinuous(t, a, 1_000_000, start, end)
	e.fundContinuous(t, b, 50_000_000, start, end+1)
	lockedB := e.locked(b)

	_, err := e.srv.TransferOwnership(e.ctx, &types.MsgTransferOwnership{GenesisAddress: mustStr(t, a), RecipientAddress: mustStr(t, b)})
	require.ErrorIs(t, err, types.ErrInvalidTransfer)
	require.Equal(t, lockedB, e.locked(b))
}

// A recipient whose own schedule has finished is an ordinary account.
func TestTransferOwnership_FinishedVestingRecipientAccepted(t *testing.T) {
	start, now, end := int64(1_000_000), int64(1_400_000), int64(2_000_000)
	e := newVestingEnv(t, now)
	a := sdk.AccAddress([]byte("genesis-A-----------"))
	b := sdk.AccAddress([]byte("recipient-B---------"))
	e.fundContinuous(t, a, 1_000_000, start, end)
	e.fundContinuous(t, b, 50_000_000, start-10, start)
	lockedA := e.locked(a)

	_, err := e.srv.TransferOwnership(e.ctx, &types.MsgTransferOwnership{GenesisAddress: mustStr(t, a), RecipientAddress: mustStr(t, b)})
	require.NoError(t, err)
	require.Equal(t, lockedA, e.locked(b))
}

// A recipient schedule that has not started yet cannot be stretched from now.
func TestTransferOwnership_RejectsNotStartedVestingRecipient(t *testing.T) {
	start, now, end := int64(1_000_000), int64(1_400_000), int64(2_000_000)
	e := newVestingEnv(t, now)
	a := sdk.AccAddress([]byte("genesis-A-----------"))
	b := sdk.AccAddress([]byte("recipient-B---------"))
	e.fundContinuous(t, a, 1_000_000, start, end)
	e.fundContinuous(t, b, 50_000_000, now+100_000, end)
	lockedB := e.locked(b)

	_, err := e.srv.TransferOwnership(e.ctx, &types.MsgTransferOwnership{GenesisAddress: mustStr(t, a), RecipientAddress: mustStr(t, b)})
	require.ErrorIs(t, err, types.ErrInvalidTransfer)
	require.Equal(t, lockedB, e.locked(b))
}

// A plain BaseVestingAccount sender must not replace the recipient's schedule either.
func TestTransferOwnership_RejectsBaseVestingSenderOntoVestingRecipient(t *testing.T) {
	start, now, end := int64(1_000_000), int64(1_400_000), int64(2_000_000)
	e := newVestingEnv(t, now)
	a := sdk.AccAddress([]byte("genesis-A-----------"))
	b := sdk.AccAddress([]byte("recipient-B---------"))
	coins := sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000)))
	base := authtypes.NewBaseAccountWithAddress(a)
	base.AccountNumber = e.ak.NextAccountNumber(e.ctx)
	bva, err := vestingtypes.NewBaseVestingAccount(base, coins, end)
	require.NoError(t, err)
	e.ak.SetAccount(e.ctx, bva)
	require.NoError(t, e.bk.MintCoins(e.ctx, minttypes.ModuleName, coins))
	require.NoError(t, e.bk.SendCoinsFromModuleToAccount(e.ctx, minttypes.ModuleName, a, coins))
	e.fundContinuous(t, b, 50_000_000, start, end)
	lockedB := e.locked(b)

	_, err = e.srv.TransferOwnership(e.ctx, &types.MsgTransferOwnership{GenesisAddress: mustStr(t, a), RecipientAddress: mustStr(t, b)})
	require.ErrorIs(t, err, types.ErrInvalidTransfer)
	require.Equal(t, lockedB, e.locked(b))
}

func mustStr(t *testing.T, a sdk.AccAddress) string {
	s, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).BytesToString(a)
	require.NoError(t, err)
	return s
}
