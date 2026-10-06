package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) StoreDevshardEscrow(ctx context.Context, escrow *types.DevshardEscrow, nextID uint64) (uint64, error) {
	return k.storeDevshardEscrow(ctx, escrow, nextID, k.GetDevshardEscrowEpochCount(ctx, escrow.EpochIndex))
}

// storeDevshardEscrow takes the epoch escrow count the caller has already read.
func (k Keeper) storeDevshardEscrow(ctx context.Context, escrow *types.DevshardEscrow, nextID uint64, epochCount uint64) (uint64, error) {
	escrow.Id = nextID

	if err := k.DevshardEscrowCounter.Set(ctx, nextID); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrows.Set(ctx, escrow.Id, *escrow); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrowsByEpoch.Set(ctx, collections.Join(escrow.EpochIndex, escrow.Id), collections.NoValue{}); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrowEpochCount.Set(ctx, escrow.EpochIndex, epochCount+1); err != nil {
		return 0, err
	}
	return escrow.Id, nil
}

func (k Keeper) GetDevshardEscrow(ctx context.Context, id uint64) (types.DevshardEscrow, bool) {
	v, err := k.DevshardEscrows.Get(ctx, id)
	if err != nil {
		return types.DevshardEscrow{}, false
	}
	if !v.Settled {
		if v.Settled, err = k.DevshardSettledEscrows.Has(ctx, id); err != nil {
			return types.DevshardEscrow{}, false
		}
	}
	return v, true
}

// MarkDevshardEscrowSettled records settlement in a key-only set instead of
// rewriting the whole escrow (~1 KB on mainnet) for one bool.
func (k Keeper) MarkDevshardEscrowSettled(ctx context.Context, id uint64) error {
	return k.DevshardSettledEscrows.Set(ctx, id)
}

func (k Keeper) SetDevshardEscrow(ctx context.Context, escrow types.DevshardEscrow) error {
	return k.DevshardEscrows.Set(ctx, escrow.Id, escrow)
}

func (k Keeper) GetDevshardEscrowEpochCount(ctx context.Context, epochIndex uint64) uint64 {
	v, err := k.DevshardEscrowEpochCount.Get(ctx, epochIndex)
	if err != nil {
		return 0
	}
	return v
}

func (k Keeper) IncrementDevshardEscrowEpochCount(ctx context.Context, epochIndex uint64) error {
	count := k.GetDevshardEscrowEpochCount(ctx, epochIndex)
	return k.DevshardEscrowEpochCount.Set(ctx, epochIndex, count+1)
}
