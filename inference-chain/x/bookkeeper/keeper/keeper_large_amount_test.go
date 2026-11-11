package keeper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
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
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bookkeeper/types"
)

type countingBank struct{ calls int }

func (b *countingBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (b *countingBank) SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) SendCoinsFromModuleToModule(context.Context, string, string, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) MintCoins(context.Context, string, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) BurnCoins(context.Context, string, sdk.Coins) error {
	b.calls++
	return nil
}
func (b *countingBank) GetDenomMetaData(context.Context, string) (banktypes.Metadata, bool) {
	return banktypes.Metadata{}, false
}

var _ types.BankKeeper = (*countingBank)(nil)

func newKeeperWithBank(t *testing.T, cfg LogConfig, out *bytes.Buffer) (Keeper, sdk.Context, *countingBank) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.MountStoreWithDB(key, storetypes.StoreTypeIAVL, db)
	require.NoError(t, ms.LoadLatestVersion())
	bank := &countingBank{}
	logger := log.NewLogger(out, log.OutputJSONOption(), log.ColorOption(false))
	authority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	k := NewKeeper(codec.NewProtoCodec(codectypes.NewInterfaceRegistry()), runtime.NewKVStoreService(key), logger, authority, bank, cfg)
	return k, sdk.NewContext(ms, cmtproto.Header{Height: 7}, false, log.NewNopLogger()), bank
}

func parseLogLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	var lines []map[string]any
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line))
		lines = append(lines, line)
	}
	require.NoError(t, scanner.Err())
	return lines
}

func TestBookkeeperLogsAmountsBeyondInt64(t *testing.T) {
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	amounts := map[string]math.Int{
		"one":         math.OneInt(),
		"max_int64":   math.NewInt(9223372036854775807),
		"max_int64+1": math.NewIntFromUint64(1 << 63),
		"ten_pow_24":  math.NewIntFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)),
		"max_uint256": math.NewIntFromBigInt(maxUint256),
	}
	denom := "ibc/C679139C206D0E7379C1526A19B91DD18A9B3C1A28E76C099962629894E4D193"
	addrA := sdk.AccAddress([]byte("genesis_addr________"))
	addrB := sdk.AccAddress([]byte("recipient_addr______"))

	ops := map[string]struct {
		bankCalls int
		subEntry  bool
		run       func(k Keeper, ctx sdk.Context, coin sdk.Coin) error
	}{
		"SendCoins": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.SendCoins(ctx, addrA, addrB, sdk.NewCoins(c), "memo")
		}},
		"SendCoinsFromAccountToModule": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.SendCoinsFromAccountToModule(ctx, addrA, "genesistransfer", sdk.NewCoins(c), "memo")
		}},
		"SendCoinsFromModuleToAccount": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.SendCoinsFromModuleToAccount(ctx, "genesistransfer", addrB, sdk.NewCoins(c), "memo")
		}},
		"SendCoinsFromModuleToModule": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.SendCoinsFromModuleToModule(ctx, "inference", "genesistransfer", sdk.NewCoins(c), "memo")
		}},
		"MintCoins": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.MintCoins(ctx, "inference", sdk.NewCoins(c), "memo")
		}},
		"BurnCoins": {1, false, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			return k.BurnCoins(ctx, "inference", sdk.NewCoins(c), "memo")
		}},
		"LogSubAccountTransaction": {0, true, func(k Keeper, ctx sdk.Context, c sdk.Coin) error {
			k.LogSubAccountTransaction(ctx, "to", "from", "sub", c, "memo")
			return nil
		}},
	}

	for amountName, amount := range amounts {
		for opName, op := range ops {
			coin := sdk.NewCoin(denom, amount)
			want := amount.String()

			t.Run(amountName+"/"+opName+"/all_logging", func(t *testing.T) {
				out := &bytes.Buffer{}
				k, ctx, bank := newKeeperWithBank(t, LogConfig{DoubleEntry: true, SimpleEntry: true, LogLevel: "info"}, out)
				require.NotPanics(t, func() { require.NoError(t, op.run(k, ctx, coin)) })
				require.Equal(t, op.bankCalls, bank.calls)
				require.Equal(t, want, coin.Amount.String())

				lines := parseLogLines(t, out)
				require.Len(t, lines, 3)
				debit, credit, simple := lines[0], lines[1], lines[2]

				require.Equal(t, "TransactionAudit", debit["message"])
				require.Equal(t, "debit", debit["type"])
				require.Equal(t, want, debit["amount"])
				require.Equal(t, want, debit["signedAmount"])
				require.Equal(t, denom, debit["denom"])

				require.Equal(t, "TransactionAudit", credit["message"])
				require.Equal(t, "credit", credit["type"])
				require.Equal(t, want, credit["amount"])
				require.Equal(t, "-"+want, credit["signedAmount"])
				require.Equal(t, denom, credit["denom"])

				msg, ok := simple["message"].(string)
				require.True(t, ok)
				if op.subEntry {
					require.True(t, strings.HasPrefix(msg, "SubAccountEntry "))
				} else {
					require.True(t, strings.HasPrefix(msg, "TransactionEntry "))
				}
				require.Contains(t, msg, want+" "+denom)
				if len(want) < 20 {
					require.Contains(t, msg, "amount="+strings.Repeat(" ", 20-len(want))+want+" ")
				} else {
					require.Contains(t, msg, "amount="+want+" ")
				}
			})

			t.Run(amountName+"/"+opName+"/logging_off", func(t *testing.T) {
				out := &bytes.Buffer{}
				k, ctx, bank := newKeeperWithBank(t, LogConfig{LogLevel: "info"}, out)
				require.NotPanics(t, func() { require.NoError(t, op.run(k, ctx, coin)) })
				require.Equal(t, op.bankCalls, bank.calls)
				require.Empty(t, parseLogLines(t, out))
			})
		}
	}
}

func TestBookkeeperSkipsZeroAmount(t *testing.T) {
	out := &bytes.Buffer{}
	k, ctx, _ := newKeeperWithBank(t, LogConfig{DoubleEntry: true, SimpleEntry: true, LogLevel: "info"}, out)
	k.LogSubAccountTransaction(ctx, "to", "from", "sub", sdk.NewCoin("ngonka", math.ZeroInt()), "memo")
	require.Empty(t, parseLogLines(t, out))
}
