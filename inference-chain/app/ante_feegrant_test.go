package app_test

import (
	"context"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/feegrant"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
)

// grantFeesUntil grants the allowance shape the v0.2.12 migration and grant-ml-ops-permissions create.
func grantFeesUntil(t *testing.T, a *app.App, payer, signer sdk.AccAddress, exp time.Time) {
	t.Helper()
	ctx := a.NewUncachedContext(false, cmtproto.Header{Height: a.LastBlockHeight(), ChainID: TallyTestChainID, Time: time.Now().UTC()})
	limit := sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(100_000_000_000)))
	require.NoError(t, a.FeeGrantKeeper.GrantAllowance(ctx, payer, signer, &feegrant.BasicAllowance{SpendLimit: limit, Expiration: &exp}))
	finalizeTxs(t, a)
}

func readGas(t *testing.T, a *app.App, read func(sdk.Context)) storetypes.Gas {
	t.Helper()
	ctx := a.NewContext(true).WithGasMeter(storetypes.NewInfiniteGasMeter())
	read(ctx)
	return ctx.GasMeter().GasConsumed()
}

// A zero-fee duty tx with a fee granter reads the grant once and leaves it untouched,
// instead of reading it twice and rewriting the same bytes.
func TestFeegrant_ZeroFeeReadsGrantOnceAndKeepsIt(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	a := f.testApp
	grantFeesUntil(t, a, f.grantee, f.granter, time.Now().UTC().Add(365*24*time.Hour))
	before, err := a.FeeGrantKeeper.GetAllowance(a.NewContext(true), f.grantee, f.granter)
	require.NoError(t, err)

	plain := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{unordered: true})
	granted := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{unordered: true, granter: f.grantee})
	gasOf := func(bz []byte) int64 {
		resp, err := a.CheckTx(&abci.RequestCheckTx{Tx: bz})
		require.NoError(t, err)
		require.Equal(t, uint32(0), resp.Code, "log=%q", resp.Log)
		return resp.GasUsed
	}
	gasOf(signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{unordered: true})) // the first CheckTx after a commit is 36 gas cheaper
	plainGas, grantedGas := gasOf(plain), gasOf(granted)

	grantRead := readGas(t, a, func(ctx sdk.Context) {
		_, err := a.FeeGrantKeeper.GetAllowance(ctx, f.grantee, f.granter)
		require.NoError(t, err)
	})
	granterRead := readGas(t, a, func(ctx sdk.Context) { require.NotNil(t, a.AccountKeeper.GetAccount(ctx, f.grantee)) })
	sizeGas := a.AccountKeeper.GetParams(a.NewContext(true)).TxSizeCostPerByte * uint64(len(granted)-len(plain))
	require.Equal(t, int64(sizeGas+grantRead+granterRead), grantedGas-plainGas, "granter costs its bytes, one grant read and the granter account read")

	resp, err := a.FinalizeBlock(&abci.RequestFinalizeBlock{Height: a.LastBlockHeight() + 1, Time: time.Now().UTC(), Txs: [][]byte{granted}})
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp.TxResults[0].Code, "log=%q", resp.TxResults[0].Log)
	var types []string
	for _, e := range resp.TxResults[0].Events {
		if e.Type == feegrant.EventTypeUpdateFeeGrant || e.Type == feegrant.EventTypeUseFeeGrant {
			types = append(types, e.Type)
		}
	}
	require.Equal(t, []string{feegrant.EventTypeUseFeeGrant, feegrant.EventTypeUpdateFeeGrant}, types, "events as from the SDK keeper")
	_, err = a.Commit()
	require.NoError(t, err)

	after, err := a.FeeGrantKeeper.GetAllowance(a.NewContext(true), f.grantee, f.granter)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// Past its expiration the grant still rejects the tx, as BasicAllowance.Accept does.
func TestFeegrant_ZeroFeeAfterExpirationRejected(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	a := f.testApp
	grantFeesUntil(t, a, f.grantee, f.granter, time.Now().UTC().Add(time.Hour))

	granted := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{granter: f.grantee})
	resp, err := a.FinalizeBlock(&abci.RequestFinalizeBlock{Height: a.LastBlockHeight() + 1, Time: time.Now().UTC().Add(2 * time.Hour), Txs: [][]byte{granted}})
	require.NoError(t, err)
	require.NotEqual(t, uint32(0), resp.TxResults[0].Code)
	require.Contains(t, resp.TxResults[0].Log, "expired")
}

// The ante chain's keeper leaves the same grant, error and events as the SDK keeper,
// and spends one grant read less (and no write when the grant is unchanged).
func TestFeegrant_WrapperMatchesSDKKeeper(t *testing.T) {
	a := createTestApp(t)
	coins := func(n int64) sdk.Coins { return sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(n))) }
	exp := time.Now().UTC().Add(365 * 24 * time.Hour)
	soon := time.Now().UTC().Add(time.Hour)
	setup := a.NewUncachedContext(false, cmtproto.Header{Height: a.LastBlockHeight(), ChainID: TallyTestChainID, Time: time.Now().UTC()})
	grants := map[string]feegrant.FeeAllowanceI{
		"basic":     &feegrant.BasicAllowance{SpendLimit: coins(1_000), Expiration: &exp},
		"unlimited": &feegrant.BasicAllowance{},
		"expiring":  &feegrant.BasicAllowance{SpendLimit: coins(1_000), Expiration: &soon},
		"periodic": &feegrant.PeriodicAllowance{
			Basic:            feegrant.BasicAllowance{SpendLimit: coins(1_000)},
			Period:           time.Hour,
			PeriodSpendLimit: coins(300),
			PeriodCanSpend:   coins(300),
			PeriodReset:      time.Now().UTC().Add(time.Hour),
		},
	}
	payers := map[string]sdk.AccAddress{}
	grantee := sdk.AccAddress([]byte("feegrant-wrapper-grantee"))
	a.AccountKeeper.SetAccount(setup, a.AccountKeeper.NewAccountWithAddress(setup, grantee))
	for name, g := range grants {
		payers[name] = sdk.AccAddress([]byte("feegrant-wrapper-" + name))
		require.NoError(t, a.FeeGrantKeeper.GrantAllowance(setup, payers[name], grantee, g))
	}
	finalizeTxs(t, a)

	grantRead := func(payer sdk.AccAddress) storetypes.Gas {
		return readGas(t, a, func(ctx sdk.Context) { _, _ = a.FeeGrantKeeper.GetAllowance(ctx, payer, grantee) })
	}
	type result struct {
		gas    storetypes.Gas
		err    string
		events sdk.Events
		grant  feegrant.FeeAllowanceI
	}
	use := func(k interface {
		UseGrantedFees(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins, []sdk.Msg) error
	}, payer sdk.AccAddress, fee sdk.Coins, at time.Time) result {
		ctx, _ := a.NewUncachedContext(false, cmtproto.Header{Height: a.LastBlockHeight() + 1, ChainID: TallyTestChainID, Time: at}).CacheContext()
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()).WithEventManager(sdk.NewEventManager())
		var r result
		if err := k.UseGrantedFees(ctx, payer, grantee, fee, nil); err != nil {
			r.err = err.Error()
		}
		r.gas = ctx.GasMeter().GasConsumed()
		r.events = ctx.EventManager().Events()
		r.grant, _ = a.FeeGrantKeeper.GetAllowance(ctx, payer, grantee)
		return r
	}

	cases := []struct {
		name, grant string
		fee         sdk.Coins
		later       time.Duration
		saved       func(read storetypes.Gas) int64 // SDK gas minus wrapper gas
	}{
		{"fee spends the limit", "basic", coins(400), 0, func(r storetypes.Gas) int64 { return int64(r) }},
		{"fee over the limit", "basic", coins(1_001), 0, func(storetypes.Gas) int64 { return 0 }},
		{"fee uses the limit up", "basic", coins(1_000), 0, func(r storetypes.Gas) int64 { return -int64(r) }},
		{"zero fee", "basic", sdk.NewCoins(), 0, nil},
		{"fee without a limit", "unlimited", coins(400), 0, nil},
		{"periodic fee", "periodic", coins(200), 0, func(r storetypes.Gas) int64 { return int64(r) }},
		{"periodic fee over the period", "periodic", coins(301), 0, func(storetypes.Gas) int64 { return 0 }},
		{"no grant", "", coins(400), 0, func(storetypes.Gas) int64 { return 0 }},
		{"expired grant", "expiring", coins(400), 2 * time.Hour, func(r storetypes.Gas) int64 { return -int64(r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payer := payers[tc.grant]
			if payer == nil {
				payer = sdk.AccAddress([]byte("feegrant-wrapper-nobody"))
			}
			at := time.Now().UTC().Add(tc.later)
			sdkRes := use(a.FeeGrantKeeper, payer, tc.fee, at)
			wrapRes := use(a.TxFeegrantKeeper(), payer, tc.fee, at)

			t.Logf("gas: sdk %d, wrapper %d", sdkRes.gas, wrapRes.gas)
			require.Equal(t, sdkRes.err, wrapRes.err)
			require.Equal(t, sdkRes.events, wrapRes.events)
			require.Equal(t, sdkRes.grant, wrapRes.grant)
			if tc.saved == nil { // unchanged grant: a single read, no write
				require.Equal(t, grantRead(payer), wrapRes.gas)
				require.Less(t, wrapRes.gas, sdkRes.gas)
				return
			}
			require.Equal(t, tc.saved(grantRead(payer)), int64(sdkRes.gas)-int64(wrapRes.gas))
		})
	}
}
