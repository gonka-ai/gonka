package app

import (
	"strings"
	"testing"

	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/CosmWasm/wasmd/x/wasm/keeper/wasmtesting"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

const testPoolAddress = "gonka1pool"

func ibcCoins(amount int64) sdk.Coins {
	return sdk.Coins{sdk.Coin{Denom: "ibc/USDT", Amount: math.NewInt(amount)}}
}

func TestLiquidityPoolBypass_DirectPoolCall(t *testing.T) {
	cases := []struct {
		name     string
		contract string
		msg      string
		funds    sdk.Coins
		want     bool
	}{
		{"purchase with ibc coin", testPoolAddress, `{"purchase_with_native":{}}`, ibcCoins(1_000_000), true},
		{"purchase with spaces", testPoolAddress, ` { "purchase_with_native" : { } } `, ibcCoins(1), true},
		{"purchase with escaped key", testPoolAddress, `{"purchase\u005fwith_native":{}}`, ibcCoins(1), true},
		{"purchase without funds", testPoolAddress, `{"purchase_with_native":{}}`, nil, false},
		{"purchase with zero amount", testPoolAddress, `{"purchase_with_native":{}}`, ibcCoins(0), false},
		{"purchase with native denom", testPoolAddress, `{"purchase_with_native":{}}`, sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1)), false},
		{"purchase with two coins", testPoolAddress, `{"purchase_with_native":{}}`, sdk.Coins{sdk.NewInt64Coin("ibc/A", 1), sdk.NewInt64Coin("ibc/B", 1)}, false},
		{"purchase with null body", testPoolAddress, `{"purchase_with_native":null}`, ibcCoins(1), false},
		{"purchase with array body", testPoolAddress, `{"purchase_with_native":[]}`, ibcCoins(1), false},
		{"purchase with body fields", testPoolAddress, `{"purchase_with_native":{"x":1}}`, ibcCoins(1), false},
		{"duplicate purchase key", testPoolAddress, `{"purchase_with_native":{},"purchase_with_native":{}}`, ibcCoins(1), false},
		{"purchase plus extra variant", testPoolAddress, `{"purchase_with_native":{},"pause":{}}`, ibcCoins(1), false},
		{"capitalized variant", testPoolAddress, `{"Purchase_with_native":{}}`, ibcCoins(1), false},
		{"trailing data", testPoolAddress, `{"purchase_with_native":{}}{}`, ibcCoins(1), false},
		{"empty object", testPoolAddress, `{}`, ibcCoins(1), false},
		{"admin pause", testPoolAddress, `{"pause":{}}`, nil, false},
		{"direct receive", testPoolAddress, `{"receive":{"sender":"a","amount":"1","msg":""}}`, nil, false},
		{"not json object", testPoolAddress, `"purchase_with_native"`, ibcCoins(1), false},
		{"oversized payload", testPoolAddress, `{"purchase_with_native":{}}` + strings.Repeat(" ", maxBypassMsgBytes), ibcCoins(1), false},
		{"other contract", "gonka1other", `{"purchase_with_native":{}}`, ibcCoins(1), false},
	}

	d := LiquidityPoolFeeBypassDecorator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := &wasmtypes.MsgExecuteContract{
				Sender:   "gonka1sender",
				Contract: tc.contract,
				Msg:      []byte(tc.msg),
				Funds:    tc.funds,
			}
			require.Equal(t, tc.want, d.matchesAllowedSwap(newTestContext(), msg, testPoolAddress, 1))
		})
	}
}

func TestLiquidityPoolBypass_Cw20SendTarget(t *testing.T) {
	cases := []struct {
		name   string
		msg    string
		wantOK bool
	}{
		{"send to pool", `{"send":{"contract":"gonka1pool","amount":"1000","msg":"e30="}}`, true},
		{"unpadded base64 msg", `{"send":{"contract":"gonka1pool","amount":"1000","msg":"e30"}}`, true},
		{"empty msg", `{"send":{"contract":"gonka1pool","amount":"1","msg":""}}`, true},
		{"zero amount", `{"send":{"contract":"gonka1pool","amount":"0","msg":"e30="}}`, false},
		{"missing amount", `{"send":{"contract":"gonka1pool","msg":"e30="}}`, false},
		{"numeric amount", `{"send":{"contract":"gonka1pool","amount":1,"msg":"e30="}}`, false},
		{"missing msg", `{"send":{"contract":"gonka1pool","amount":"1"}}`, false},
		{"invalid base64 msg", `{"send":{"contract":"gonka1pool","amount":"1","msg":"!!"}}`, false},
		{"null contract", `{"send":{"contract":null,"amount":"1","msg":""}}`, false},
		{"missing contract", `{"send":{"amount":"1","msg":""}}`, false},
		{"capitalized fields", `{"send":{"Contract":"gonka1pool","Amount":"1","msg":""}}`, false},
		{"unknown field", `{"send":{"contract":"gonka1pool","amount":"1","msg":"","memo":""}}`, false},
		{"duplicate amount", `{"send":{"contract":"gonka1pool","amount":"1","amount":"0","msg":""}}`, false},
		{"duplicate send", `{"send":{"contract":"gonka1other","amount":"1","msg":""},"send":{"contract":"gonka1pool","amount":"1","msg":""}}`, false},
		{"extra variant", `{"send":{"contract":"gonka1pool","amount":"1","msg":""},"burn":{"amount":"1"}}`, false},
		{"transfer", `{"transfer":{"recipient":"gonka1pool","amount":"1"}}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := cw20SendTarget([]byte(tc.msg))
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, testPoolAddress, target)
			} else {
				require.Empty(t, target)
			}
		})
	}
}

func TestLiquidityPoolBypass_IsPositiveUint256(t *testing.T) {
	maxU256 := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	cases := map[string]bool{
		"1":                           true,
		"1000":                        true,
		"08":                          true,
		"010":                         true,
		"000000000000000000000000001": true,
		maxU256:                       true,
		"0":                           false,
		"00":                          false,
		"":                            false,
		"+1":                          false,
		"-1":                          false,
		"0x10":                        false,
		"0b10":                        false,
		"1_000":                       false,
		"1.0":                         false,
		"1e3":                         false,
		" 1":                          false,
		"115792089237316195423570985008687907853269984665640564039457584007913129639936": false,
		strings.Repeat("9", maxUint256Digits+1):                                          false,
		strings.Repeat("0", 1000) + "1":                                                  true,
	}
	for in, want := range cases {
		require.Equal(t, want, isPositiveUint256(in), "input %q", in)
	}
}

func TestLiquidityPoolBypass_OversizedAmountIsCheap(t *testing.T) {
	huge := `{"send":{"contract":"gonka1pool","amount":"` + strings.Repeat("9", 1_000_000) + `","msg":""}}`
	allocs := testing.AllocsPerRun(5, func() {
		_, ok := cw20SendTarget([]byte(huge))
		require.False(t, ok)
	})
	require.Less(t, allocs, float64(10))
	require.False(t, isPositiveUint256(strings.Repeat("9", 1_000_000)))
}

func TestLiquidityPoolBypass_Cw20SendFromWrappedToken(t *testing.T) {
	cfg := sdk.GetConfig()
	accPrefix, accPubPrefix := cfg.GetBech32AccountAddrPrefix(), cfg.GetBech32AccountPubPrefix()
	cacheEnabled := sdk.IsAddrCacheEnabled()
	sdk.SetAddrCacheEnabled(false)
	cfg.SetBech32PrefixForAccount(sdk.Bech32MainPrefix, sdk.Bech32PrefixAccPub)
	t.Cleanup(func() {
		cfg.SetBech32PrefixForAccount(accPrefix, accPubPrefix)
		sdk.SetAddrCacheEnabled(cacheEnabled)
	})

	mock := wasmtesting.MockWasmEngine{}
	wasmtesting.MakeInstantiable(&mock)
	ctx, keepers := wasmkeeper.CreateTestInput(t, false, wasmkeeper.BuiltInCapabilities(), wasmkeeper.WithWasmEngine(&mock))

	wrapped := wasmkeeper.SeedNewContractInstance(t, ctx, keepers, &mock)
	other := wasmkeeper.SeedNewContractInstance(t, ctx, keepers, &mock)
	require.NotEqual(t, wrapped.CodeID, other.CodeID)

	d := LiquidityPoolFeeBypassDecorator{WasmKeeper: keepers.WasmKeeper}
	send := func(contract sdk.AccAddress, msg string) *wasmtypes.MsgExecuteContract {
		return &wasmtypes.MsgExecuteContract{Sender: "gonka1sender", Contract: contract.String(), Msg: []byte(msg)}
	}
	toPool := `{"send":{"contract":"gonka1pool","amount":"1000","msg":"e30="}}`

	require.True(t, d.matchesAllowedSwap(ctx, send(wrapped.Contract, toPool), testPoolAddress, wrapped.CodeID))
	require.False(t, d.matchesAllowedSwap(ctx, send(other.Contract, toPool), testPoolAddress, wrapped.CodeID),
		"cw20 contract with a different code id")
	require.False(t, d.matchesAllowedSwap(ctx,
		send(wrapped.Contract, `{"send":{"contract":"gonka1other","amount":"1000","msg":"e30="}}`), testPoolAddress, wrapped.CodeID),
		"send to a different contract")
	require.False(t, d.matchesAllowedSwap(ctx,
		send(wrapped.Contract, `{"send":{"contract":"gonka1pool","amount":"0","msg":"e30="}}`), testPoolAddress, wrapped.CodeID),
		"zero amount")
}
