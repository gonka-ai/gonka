package app

import (
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
