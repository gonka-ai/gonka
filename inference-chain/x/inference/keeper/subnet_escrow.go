package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) StoreSubnetEscrow(ctx context.Context, escrow *types.SubnetEscrow, nextID uint64) (uint64, error) {
	escrow.Id = nextID

	if err := k.SubnetEscrowCounter.Set(ctx, nextID); err != nil {
		return 0, err
	}
	if err := k.SubnetEscrows.Set(ctx, escrow.Id, *escrow); err != nil {
		return 0, err
	}
	if err := k.SubnetEscrowsByEpoch.Set(ctx, collections.Join(escrow.EpochIndex, escrow.Id), collections.NoValue{}); err != nil {
		return 0, err
	}
	if err := k.IncrementSubnetEscrowEpochCount(ctx, escrow.EpochIndex); err != nil {
		return 0, err
	}
	return escrow.Id, nil
}

func (k Keeper) GetSubnetEscrow(ctx context.Context, id uint64) (types.SubnetEscrow, bool) {
	v, err := k.SubnetEscrows.Get(ctx, id)
	if err != nil {
		return types.SubnetEscrow{}, false
	}
	return v, true
}

func (k Keeper) SetSubnetEscrow(ctx context.Context, escrow types.SubnetEscrow) error {
	return k.SubnetEscrows.Set(ctx, escrow.Id, escrow)
}

func (k Keeper) GetSubnetEscrowEpochCount(ctx context.Context, epochIndex uint64) uint64 {
	v, err := k.SubnetEscrowEpochCount.Get(ctx, epochIndex)
	if err != nil {
		return 0
	}
	return v
}

func (k Keeper) IncrementSubnetEscrowEpochCount(ctx context.Context, epochIndex uint64) error {
	count := k.GetSubnetEscrowEpochCount(ctx, epochIndex)
	return k.SubnetEscrowEpochCount.Set(ctx, epochIndex, count+1)
}
