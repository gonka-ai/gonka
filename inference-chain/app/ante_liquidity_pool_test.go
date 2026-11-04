package app

import (
	"strings"
	"testing"

	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/CosmWasm/wasmd/x/wasm/keeper/wasmtesting"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
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
		{"direct receive", testPoolAddress, `{"receive":{"sender":"a","amount":"1","msg":"e30="}}`, nil, false},
		{"not json object", testPoolAddress, `"purchase_with_native"`, ibcCoins(1), false},
		{"oversized payload", testPoolAddress, `{"purchase_with_native":{}}` + strings.Repeat(" ", maxBypassMsgBytes), ibcCoins(1), false},
		{"other contract", "gonka1other", `{"purchase_with_native":{}}`, ibcCoins(1), false},
		{"unapproved ibc denom", testPoolAddress, `{"purchase_with_native":{}}`, sdk.NewCoins(sdk.NewInt64Coin("ibc/NOTAPPROVED", 1)), false},
	}

	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetIBCTradeApprovedToken(ctx, inferencetypes.BridgeTokenReference{
		ChainId:         "cosmoshub-4",
		ContractAddress: "ibc/USDT",
	}))
	require.NoError(t, k.SetIBCTokenMetadata(ctx, "cosmoshub-4", "ibc/USDT", inferencetypes.BridgeTokenMetadata{
		Name: "Tether", Symbol: "USDT", Decimals: 6,
	}))
	d := LiquidityPoolFeeBypassDecorator{InferenceKeeper: &k}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := &wasmtypes.MsgExecuteContract{
				Sender:   "gonka1sender",
				Contract: tc.contract,
				Msg:      []byte(tc.msg),
				Funds:    tc.funds,
			}
			require.Equal(t, tc.want, d.matchesAllowedSwap(ctx, msg, testPoolAddress, 1))
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
		{"padded hook with whitespace", `{"send":{"contract":"gonka1pool","amount":"1","msg":"IHsgfSA="}}`, true},
		{"empty msg", `{"send":{"contract":"gonka1pool","amount":"1","msg":""}}`, false},
		{"non-json hook", `{"send":{"contract":"gonka1pool","amount":"1","msg":"bm90IGpzb24="}}`, false},
		{"non-empty hook", `{"send":{"contract":"gonka1pool","amount":"1","msg":"eyJhIjoxfQ=="}}`, false},
		{"array hook", `{"send":{"contract":"gonka1pool","amount":"1","msg":"W10="}}`, false},
		{"hook with trailing data", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e317fQ=="}}`, false},
		{"zero amount", `{"send":{"contract":"gonka1pool","amount":"0","msg":"e30="}}`, false},
		{"missing amount", `{"send":{"contract":"gonka1pool","msg":"e30="}}`, false},
		{"numeric amount", `{"send":{"contract":"gonka1pool","amount":1,"msg":"e30="}}`, false},
		{"missing msg", `{"send":{"contract":"gonka1pool","amount":"1"}}`, false},
		{"invalid base64 msg", `{"send":{"contract":"gonka1pool","amount":"1","msg":"!!"}}`, false},
		{"padded msg with nonzero unused bits", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e31="}}`, false},
		{"unpadded msg with nonzero unused bits", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e31"}}`, false},
		{"msg with embedded LF", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e3\n0="}}`, false},
		{"msg with trailing CRLF", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e30=\r\n"}}`, false},
		{"unpadded msg with trailing LF", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e30\n"}}`, false},
		{"null contract", `{"send":{"contract":null,"amount":"1","msg":"e30="}}`, false},
		{"missing contract", `{"send":{"amount":"1","msg":"e30="}}`, false},
		{"capitalized fields", `{"send":{"Contract":"gonka1pool","Amount":"1","msg":"e30="}}`, false},
		{"unknown field", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e30=","memo":""}}`, false},
		{"duplicate amount", `{"send":{"contract":"gonka1pool","amount":"1","amount":"2","msg":"e30="}}`, false},
		{"duplicate send", `{"send":{"contract":"gonka1other","amount":"1","msg":"e30="},"send":{"contract":"gonka1pool","amount":"1","msg":"e30="}}`, false},
		{"extra variant", `{"send":{"contract":"gonka1pool","amount":"1","msg":"e30="},"burn":{"amount":"1"}}`, false},
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
	huge := `{"send":{"contract":"gonka1pool","amount":"` + strings.Repeat("9", 1_000_000) + `","msg":"e30="}}`
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

	require.False(t, d.matchesAllowedSwap(ctx, send(wrapped.Contract, toPool), testPoolAddress, wrapped.CodeID),
		"code id alone is not a registered wrapped instance")
	require.False(t, d.matchesAllowedSwap(ctx, send(other.Contract, toPool), testPoolAddress, wrapped.CodeID),
		"cw20 contract with a different code id")
	require.False(t, d.matchesAllowedSwap(ctx,
		send(wrapped.Contract, `{"send":{"contract":"gonka1other","amount":"1000","msg":"e30="}}`), testPoolAddress, wrapped.CodeID),
		"send to a different contract")
	require.False(t, d.matchesAllowedSwap(ctx,
		send(wrapped.Contract, `{"send":{"contract":"gonka1pool","amount":"0","msg":"e30="}}`), testPoolAddress, wrapped.CodeID),
		"zero amount")
}

func TestLiquidityPoolBypass_AnteHandle(t *testing.T) {
	const gasCap = 200_000
	purchase := func(msg string) *wasmtypes.MsgExecuteContract {
		return &wasmtypes.MsgExecuteContract{
			Sender:   "gonka1sender",
			Contract: testPoolAddress,
			Msg:      []byte(msg),
			Funds:    ibcCoins(1_000),
		}
	}
	canonical := `{"purchase_with_native":{}}`
	atLimit := canonical + strings.Repeat(" ", maxBypassMsgBytes-len(canonical))
	exec := authztypes.NewMsgExec(sdk.AccAddress("granteeaddr________"), []sdk.Msg{purchase(canonical)})

	cases := []struct {
		name         string
		skipPool     bool
		skipWrapped  bool
		msgs         []sdk.Msg
		gas          uint64
		wantBypass   bool
		wantErrorMsg string
	}{
		{name: "single purchase", msgs: []sdk.Msg{purchase(canonical)}, gas: gasCap, wantBypass: true},
		{name: "two purchases", msgs: []sdk.Msg{purchase(canonical), purchase(canonical)}, gas: 100_000, wantBypass: true},
		{name: "payload at size limit", msgs: []sdk.Msg{purchase(atLimit)}, gas: 100_000, wantBypass: true},
		{name: "payload over size limit", msgs: []sdk.Msg{purchase(atLimit + " ")}, gas: 100_000},
		{name: "pool not registered", skipPool: true, msgs: []sdk.Msg{purchase(canonical)}, gas: 100_000},
		{name: "wrapped code id not registered", skipWrapped: true, msgs: []sdk.Msg{purchase(canonical)}, gas: 100_000},
		{name: "purchase plus admin call", msgs: []sdk.Msg{purchase(canonical), purchase(`{"pause":{}}`)}, gas: 100_000},
		{name: "purchase plus bank send", msgs: []sdk.Msg{purchase(canonical), &banktypes.MsgSend{}}, gas: 100_000},
		{name: "purchase wrapped in authz exec", msgs: []sdk.Msg{&exec}, gas: 100_000},
		{name: "gas over cap", msgs: []sdk.Msg{purchase(canonical)}, gas: gasCap + 1, wantErrorMsg: "exceeds cap"},
		{name: "gas over cap on non-eligible tx", msgs: []sdk.Msg{purchase(`{"pause":{}}`)}, gas: gasCap + 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := keepertest.InferenceKeeper(t)
			if !tc.skipPool {
				require.NoError(t, k.SetLiquidityPool(ctx, inferencetypes.LiquidityPool{Address: testPoolAddress, CodeId: 2}))
			}
			if !tc.skipWrapped {
				require.NoError(t, k.SetWrappedTokenCodeID(ctx, 1))
			}
			require.NoError(t, k.SetIBCTradeApprovedToken(ctx, inferencetypes.BridgeTokenReference{
				ChainId:         "cosmoshub-4",
				ContractAddress: "ibc/USDT",
			}))
			require.NoError(t, k.SetIBCTokenMetadata(ctx, "cosmoshub-4", "ibc/USDT", inferencetypes.BridgeTokenMetadata{
				Name: "Tether", Symbol: "USDT", Decimals: 6,
			}))
			ctx = ctx.WithMinGasPrices(sdk.NewDecCoins(sdk.NewInt64DecCoin("ngonka", 1)))

			d := LiquidityPoolFeeBypassDecorator{InferenceKeeper: &k, GasCap: gasCap}
			called := false
			_, err := d.AnteHandle(ctx, testFeeTx{msgs: tc.msgs, gas: tc.gas}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
				called = true
				require.Equal(t, tc.wantBypass, IsNetworkDutyBypassed(ctx))
				require.Equal(t, tc.wantBypass, ctx.MinGasPrices().IsZero())
				return ctx, nil
			})
			if tc.wantErrorMsg != "" {
				require.ErrorContains(t, err, tc.wantErrorMsg)
				require.False(t, called)
				return
			}
			require.NoError(t, err)
			require.True(t, called)
		})
	}
}
