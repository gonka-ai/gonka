package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetDelegationRewardTransferSnapshot(ctx context.Context, snapshot types.DelegationRewardTransferSnapshot) error {
	return k.DelegationRewardTransferSnapshot.Set(ctx, snapshot.EpochIndex, snapshot)
}

func (k Keeper) GetDelegationRewardTransferSnapshot(ctx context.Context, epochIndex uint64) (types.DelegationRewardTransferSnapshot, bool) {
	snapshot, err := k.DelegationRewardTransferSnapshot.Get(ctx, epochIndex)
	if err != nil {
		return types.DelegationRewardTransferSnapshot{}, false
	}
	return snapshot, true
}

func (k Keeper) RemoveDelegationRewardTransferSnapshot(ctx context.Context, epochIndex uint64) error {
	return k.DelegationRewardTransferSnapshot.Remove(ctx, epochIndex)
}

func (k Keeper) GetDelegationRewardTransfersForEpoch(ctx context.Context, epochIndex uint64) ([]*types.DelegationRewardTransfer, error) {
	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx, epochIndex)
	if !found {
		return nil, nil
	}
	transfers := make([]*types.DelegationRewardTransfer, len(snapshot.Transfers))
	copy(transfers, snapshot.Transfers)
	return transfers, nil
}

func (k Keeper) GetDelegationRewardPenaltiesForEpoch(ctx context.Context, epochIndex uint64) ([]*types.DelegationRewardPenalty, error) {
	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx, epochIndex)
	if !found {
		return nil, nil
	}
	penalties := make([]*types.DelegationRewardPenalty, len(snapshot.Penalties))
	copy(penalties, snapshot.Penalties)
	return penalties, nil
}
