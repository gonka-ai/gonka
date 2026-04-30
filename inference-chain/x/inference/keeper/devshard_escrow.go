package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) StoreDevshardEscrow(ctx context.Context, escrow *types.DevshardEscrow, nextID uint64) (uint64, error) {
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
	if err := k.IncrementDevshardEscrowEpochCount(ctx, escrow.EpochIndex); err != nil {
		return 0, err
	}
	return escrow.Id, nil
}

func (k Keeper) GetDevshardEscrow(ctx context.Context, id uint64) (types.DevshardEscrow, bool) {
	v, err := k.DevshardEscrows.Get(ctx, id)
	if err != nil {
		return types.DevshardEscrow{}, false
	}
	return v, true
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
