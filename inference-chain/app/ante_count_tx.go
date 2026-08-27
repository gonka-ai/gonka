package app

import (
	"encoding/binary"

	corestoretypes "cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// CountTXSimulateGasDecorator wraps wasmd CountTXDecorator so Simulate meters
// the same TXCounterPrefix Get+Set that FinalizeBlock pays, without assigning
// CosmWasm env.transaction.index.
//
// wasmd skips the whole decorator when simulate=true ("Simulations don't get a
// tx counter value assigned"). That is correct for the Wasm env, but DAPI sizes
// gasWanted from Simulate gas_used, so the skipped KV showed up as a
// Simulate-versus-Finalize hole (HardwareRelabelTests: 38_627 vs 48_212).
//
// CheckTx and Finalize still go through wasmd unchanged (counter assigned).
// The Simulate Get+Set is on BaseApp's cache and is discarded.
//
// TODO(simulate-gas): After a small HardwareDiff re-measure (Simulate gas_used
// vs the tx's FinalizeBlock gas_used), drop DAPI's 1.5× HardwareDiff
// multiplier in gasWantedFromSimulate if the remaining gap fits in 1.2×.
// TODO(simulate-gas): Gating CountTX to wasm messages is a separate fee cut
// (duty txs never read env.transaction); do not mix it with this metering.
type CountTXSimulateGasDecorator struct {
	inner sdk.AnteDecorator
	store corestoretypes.KVStoreService
}

func NewCountTXSimulateGasDecorator(s corestoretypes.KVStoreService) CountTXSimulateGasDecorator {
	return CountTXSimulateGasDecorator{
		inner: wasmkeeper.NewCountTXDecorator(s),
		store: s,
	}
}

func (d CountTXSimulateGasDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if !simulate {
		return d.inner.AnteHandle(ctx, tx, simulate, next)
	}
	if err := d.meterCounterKV(ctx); err != nil {
		return ctx, err
	}
	// Do not WithTXCounter: CosmWasm env.transaction stays unset in Simulate.
	return next(ctx, tx, simulate)
}

func (d CountTXSimulateGasDecorator) meterCounterKV(ctx sdk.Context) error {
	if d.store == nil {
		return nil
	}
	store := d.store.OpenKVStore(ctx)
	currentHeight := ctx.BlockHeight()

	var txCounter uint32
	bz, err := store.Get(wasmtypes.TXCounterPrefix)
	if err != nil {
		return errorsmod.Wrap(err, "read tx counter")
	}
	if bz != nil {
		lastHeight, val := decodeHeightCounter(bz)
		if currentHeight == lastHeight {
			txCounter = val
		}
	}
	// Encoding must stay in sync with wasmd x/wasm/keeper/ante.go.
	if err := store.Set(wasmtypes.TXCounterPrefix, encodeHeightCounter(currentHeight, txCounter+1)); err != nil {
		return errorsmod.Wrap(err, "store tx counter")
	}
	return nil
}

func encodeHeightCounter(height int64, counter uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, counter)
	return append(sdk.Uint64ToBigEndian(uint64(height)), b...)
}

func decodeHeightCounter(bz []byte) (int64, uint32) {
	return int64(sdk.BigEndianToUint64(bz[0:8])), binary.BigEndian.Uint32(bz[8:])
}
