package app

import (
	"encoding/binary"

	corestoretypes "cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// CountTXSimulateGasDecorator exists because wasmd CountTXDecorator returns
// before the TXCounterPrefix Get+Set when simulate=true, so Simulate gas_used
// omits KV that FinalizeBlock still pays. DAPI sets gasWanted from Simulate;
// that hole OOGd small HardwareDiff at 1.2× (HardwareRelabelTests: 38_627 sim
// vs 48_212 deliver).
//
// Simulate: same Get+Set on BaseApp's discarded cache, without WithTXCounter
// so CosmWasm env.transaction stays unset. CheckTx/Finalize: wasmd unchanged.
//
// Remove when wasmd meters that KV during Simulate and still does not assign
// env.transaction. Replace this with wasmkeeper.NewCountTXDecorator in
// NewAnteHandler. Confirm HardwareRelabelTests still passes at 1.2×
// (gasWantedFromSimulate).
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
