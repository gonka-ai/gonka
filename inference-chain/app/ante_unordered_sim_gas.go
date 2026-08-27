package app

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// unorderedNonceAdder is the AccountKeeper surface used to meter unordered
// nonce KV during Simulate. *authkeeper.Keeper implements this.
type unorderedNonceAdder interface {
	TryAddUnorderedNonce(ctx sdk.Context, sender []byte, timestamp time.Time) error
}

// UnorderedNonceSimGasDecorator meters TryAddUnorderedNonce during Simulate.
//
// SDK SigVerificationDecorator.verifyUnorderedNonce returns before
// TryAddUnorderedNonce when ExecModeSimulate so a duplicate timeout does not
// fail gas estimation. Finalize still does the Has+Set. DAPI sizes gasWanted
// from Simulate, so that skip was part of the HardwareRelabelTests
// Simulate-versus-Finalize hole.
//
// This decorator runs only when simulate=true, on the cache (discarded).
// Duplicate "already used timeout" is ignored so Simulate still succeeds.
// CheckTx and Finalize do not run it; the SDK decorator owns the real insert.
//
// TODO(simulate-gas): Remove this decorator if gonka-ai/cosmos-sdk starts
// running TryAddUnorderedNonce during Simulate — otherwise Simulate would
// double-meter the nonce KV. Re-measure a small HardwareDiff after removal.
// TODO(simulate-gas): WithUnorderedTxGasCost is 0 on both paths and does not
// close this hole; do not restore the 2240 default as a substitute.
type UnorderedNonceSimGasDecorator struct {
	ak unorderedNonceAdder
}

func NewUnorderedNonceSimGasDecorator(ak unorderedNonceAdder) UnorderedNonceSimGasDecorator {
	return UnorderedNonceSimGasDecorator{ak: ak}
}

func (d UnorderedNonceSimGasDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if !simulate || d.ak == nil {
		return next(ctx, tx, simulate)
	}
	utx, ok := tx.(sdk.TxWithUnordered)
	if !ok || !utx.GetUnordered() {
		return next(ctx, tx, simulate)
	}
	sigTx, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return next(ctx, tx, simulate)
	}
	signers, err := sigTx.GetSigners()
	if err != nil {
		// Decode failures still belong to SigVerificationDecorator.
		return next(ctx, tx, simulate)
	}
	timeout := utx.GetTimeoutTimeStamp()
	for _, signer := range signers {
		_ = d.ak.TryAddUnorderedNonce(ctx, signer, timeout)
	}
	return next(ctx, tx, simulate)
}
