package app

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// feeDeductAccountGetter is the AccountKeeper surface used to find the fee
// payer during Simulate. *authkeeper.Keeper implements this.
type feeDeductAccountGetter interface {
	GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI
}

// FeeDeductSimGasDecorator exists because SDK DeductFeeDecorator.checkDeductFee
// skips DeductFees when the fee is zero, and `--gas auto` simulates with a zero
// fee (fee = gas-prices x gas, and gas is still 0 while it is being estimated).
// Finalize then pays gas x price, so the bank transfer to the fee collector is
// charged only there: ~24k gas on a paid MsgVote (#2300), enough to push it
// past `--gas-adjustment 1.5`. Same Simulate-versus-Finalize hole as
// CountTXSimulateGasDecorator and UnorderedNonceSimGasDecorator.
//
// Simulate-only, zero-fee only, not for fee-bypassed txs (they pay nothing in
// Finalize either): send 1ngonka from the account DeductFeeDecorator would charge
// (the fee granter if set, else the fee payer) to the fee collector on the
// discarded cache, and ignore the result. A payer that cannot cover it fails in
// Finalize anyway. CheckTx/Finalize: DeductFeeDecorator still owns the real transfer.
//
// Remove when gonka-ai/cosmos-sdk meters the fee transfer during Simulate.
// Delete this type and its NewAnteHandler entry.
type FeeDeductSimGasDecorator struct {
	ak feeDeductAccountGetter
	bk authtypes.BankKeeper
}

func NewFeeDeductSimGasDecorator(ak feeDeductAccountGetter, bk authtypes.BankKeeper) FeeDeductSimGasDecorator {
	return FeeDeductSimGasDecorator{ak: ak, bk: bk}
}

// simulatedFeeDeduction is the stand-in fee. Its size barely moves the gas: a
// balance's stored bytes grow by one per decimal digit.
var simulatedFeeDeduction = sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 1))

func (d FeeDeductSimGasDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if !simulate || d.ak == nil || d.bk == nil || IsNetworkDutyBypassed(ctx) {
		return next(ctx, tx, simulate)
	}
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok || !feeTx.GetFee().IsZero() {
		// A non-zero fee is already deducted by DeductFeeDecorator in Simulate.
		return next(ctx, tx, simulate)
	}
	deductFrom := sdk.AccAddress(feeTx.FeePayer())
	if granter := feeTx.FeeGranter(); granter != nil {
		deductFrom = sdk.AccAddress(granter)
	}
	if len(deductFrom) == 0 {
		return next(ctx, tx, simulate)
	}
	if acc := d.ak.GetAccount(ctx, deductFrom); acc != nil {
		_ = ante.DeductFees(d.bk, ctx, acc, simulatedFeeDeduction)
	}
	return next(ctx, tx, simulate)
}
