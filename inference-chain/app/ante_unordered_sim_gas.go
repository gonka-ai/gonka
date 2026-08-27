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

// UnorderedNonceSimGasDecorator exists because SDK
// SigVerificationDecorator.verifyUnorderedNonce returns before
// TryAddUnorderedNonce in ExecModeSimulate (duplicate timeout must not fail
// gas estimation). Finalize still does the Has+Set. Same Simulate-versus-
// Finalize hole as CountTXSimulateGasDecorator; together they let
// HardwareRelabelTests pass at 1.2× (38_627 sim vs 48_212 deliver).
//
// Simulate-only: TryAdd on the discarded cache, ignore "already used timeout".
// CheckTx/Finalize: SDK still owns the real insert.
//
// Remove when gonka-ai/cosmos-sdk meters TryAdd during Simulate and still
// ignores duplicate errors. Delete this type and its NewAnteHandler entry.
// Do not substitute WithUnorderedTxGasCost(2240) — that charge already runs
// on both paths and does not replace the KV. Confirm HardwareRelabelTests
// still passes at 1.2× and Simulate of an already-used timeout still succeeds.
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
