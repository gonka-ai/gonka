package keeper

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestChargePoCV2StoreCommitGas_FirstCommitChargesBaseOnce(t *testing.T) {
	feeParams := &types.FeeParams{
		BaseValidationGas: 1_000,
		GasPerPocCount:    10,
	}

	ctx := sdk.Context{}.WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	err := chargePoCV2StoreCommitGas(ctx, feeParams, true, 8)
	require.NoError(t, err)
	require.Equal(t, storetypes.Gas(1_080), ctx.GasMeter().GasConsumed())

	noFeeCtx := sdk.Context{}.WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	err = chargePoCV2StoreCommitGas(noFeeCtx, nil, true, 8)
	require.NoError(t, err)
	require.Equal(t, storetypes.Gas(0), noFeeCtx.GasMeter().GasConsumed())
}

func TestChargePoCV2StoreCommitGas_AggregatesDeltaAcrossModels(t *testing.T) {
	feeParams := &types.FeeParams{
		BaseValidationGas: 1_000,
		GasPerPocCount:    10,
	}

	ctx := sdk.Context{}.WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	err := chargePoCV2StoreCommitGas(ctx, feeParams, false, 15)
	require.NoError(t, err)
	require.Equal(t, storetypes.Gas(150), ctx.GasMeter().GasConsumed())

	noFeeCtx := sdk.Context{}.WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	err = chargePoCV2StoreCommitGas(noFeeCtx, nil, false, 15)
	require.NoError(t, err)
	require.Equal(t, storetypes.Gas(0), noFeeCtx.GasMeter().GasConsumed())
}
