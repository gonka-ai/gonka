package app

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Ante (bypass, repeated_len, fee checker) plus one handler read share a single params KV read.
func TestTxParamsCacheDecorator_OneReadPerTx(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params := inferencetypes.DefaultParams()
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}},
		fee:  sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000_000))),
		gas:  1_000_000,
	}
	checker := GonkaFeeChecker(&k)
	handler := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		if _, _, err := checker(ctx, tx); err != nil {
			return ctx, err
		}
		_, err := k.GetParams(ctx) // msg handler
		return ctx, err
	}

	run := func(cached bool) storetypes.Gas {
		decs := []sdk.AnteDecorator{
			NetworkDutyFeeBypassDecorator{InferenceKeeper: &k},
			FeeGroupRepeatedLenDecorator{InferenceKeeper: &k},
		}
		if cached {
			decs = append([]sdk.AnteDecorator{TxParamsCacheDecorator{}}, decs...)
		}
		decs = append(decs, terminal(handler))
		c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err := sdk.ChainAnteDecorators(decs...)(c, tx, false)
		require.NoError(t, err)
		return c.GasMeter().GasConsumed()
	}

	one := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := k.GetParams(one)
	require.NoError(t, err)
	read := one.GasMeter().GasConsumed()

	plain, cached := run(false), run(true)
	require.Equal(t, 2*read, plain-cached, "repeated_len, fee checker and handler: one params read instead of three")
}

type terminal func(sdk.Context, sdk.Tx, bool) (sdk.Context, error)

func (h terminal) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, _ sdk.AnteHandler) (sdk.Context, error) {
	return h(ctx, tx, simulate)
}

type countingAuthKeeper struct {
	ante.AccountKeeper
	reads    int
	accReads int
	accounts map[string]sdk.AccountI
}

func (k *countingAuthKeeper) GetAccount(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	k.accReads++
	if acc, ok := k.accounts[string(addr)]; ok {
		return acc
	}
	return nil
}

func (k *countingAuthKeeper) SetAccount(_ context.Context, acc sdk.AccountI) {
	k.accounts[string(acc.GetAddress())] = acc
}

func (k *countingAuthKeeper) GetParams(context.Context) authtypes.Params {
	k.reads++
	return authtypes.DefaultParams()
}

// The SDK ante decorators read x/auth params three or four times per tx; behind
// TxParamsCacheDecorator they share one read.
func TestTxAuthKeeper_ParamsOneReadPerTx(t *testing.T) {
	_, ctx := keepertest.InferenceKeeper(t)
	inner := &countingAuthKeeper{}
	ak := txAuthKeeper{inner}
	readFour := terminal(func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		for i := 0; i < 4; i++ {
			require.Equal(t, authtypes.DefaultParams(), ak.GetParams(ctx))
		}
		return ctx, nil
	})

	_, err := sdk.ChainAnteDecorators(TxParamsCacheDecorator{}, readFour)(ctx, testFeeTx{}, false)
	require.NoError(t, err)
	require.Equal(t, 1, inner.reads)

	_, err = sdk.ChainAnteDecorators(TxParamsCacheDecorator{}, readFour)(ctx, testFeeTx{}, false)
	require.NoError(t, err)
	require.Equal(t, 2, inner.reads, "the cache lives for one tx")

	_, err = readFour(ctx, testFeeTx{}, false)
	require.NoError(t, err)
	require.Equal(t, 6, inner.reads, "no cache installed: every call reads")
}

// Signer accounts: one store read per tx, SetAccount keeps the cached copy
// current, a missing account is not cached.
func TestTxAuthKeeper_AccountsOneReadPerTx(t *testing.T) {
	_, ctx := keepertest.InferenceKeeper(t)
	signer := sdk.AccAddress("signer______________")
	missing := sdk.AccAddress("missing_____________")
	inner := &countingAuthKeeper{accounts: map[string]sdk.AccountI{
		string(signer): authtypes.NewBaseAccount(signer, nil, 7, 3),
	}}
	ak := txAuthKeeper{inner}
	body := terminal(func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		for i := 0; i < 4; i++ {
			require.Equal(t, uint64(3), ak.GetAccount(ctx, signer).GetSequence())
		}
		acc := ak.GetAccount(ctx, signer)
		require.NoError(t, acc.SetSequence(4))
		ak.SetAccount(ctx, acc)
		require.Equal(t, uint64(4), ak.GetAccount(ctx, signer).GetSequence())
		require.Nil(t, ak.GetAccount(ctx, missing))
		require.Nil(t, ak.GetAccount(ctx, missing))
		return ctx, nil
	})

	_, err := sdk.ChainAnteDecorators(TxParamsCacheDecorator{}, body)(ctx, testFeeTx{}, false)
	require.NoError(t, err)
	require.Equal(t, 3, inner.accReads, "signer once, missing account every time")
	require.Equal(t, uint64(4), inner.accounts[string(signer)].GetSequence())
}
