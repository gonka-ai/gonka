package app

import (
	"encoding/binary"

	storetypes "cosmossdk.io/store/types"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// CountTXDecorator replaces wasmd's CountTXDecorator: the tx position in the
// block lives in a transient store (reset on Commit) instead of a persistent
// wasm-store key rewritten by every tx. Same counter, transient gas.
// Simulate meters the same Get+Set but leaves env.transaction unset.
type CountTXDecorator struct {
	key storetypes.StoreKey
}

func NewCountTXDecorator(key storetypes.StoreKey) CountTXDecorator {
	return CountTXDecorator{key: key}
}

func (d CountTXDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	store := ctx.TransientStore(d.key)
	var txCounter uint32
	if bz := store.Get(inferencetypes.TransientTxCounterKey); len(bz) == 4 {
		txCounter = binary.BigEndian.Uint32(bz)
	}
	next4 := make([]byte, 4)
	binary.BigEndian.PutUint32(next4, txCounter+1)
	store.Set(inferencetypes.TransientTxCounterKey, next4)
	if simulate {
		return next(ctx, tx, simulate)
	}
	return next(wasmtypes.WithTXCounter(ctx, txCounter), tx, simulate)
}
