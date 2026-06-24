package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetDelegationRewardTransferSnapshot(ctx context.Context, snapshot types.DelegationRewardTransferSnapshot) error {
	return k.DelegationRewardTransferSnapshot.Set(ctx, snapshot)
}

func (k Keeper) GetDelegationRewardTransferSnapshot(ctx context.Context) (types.DelegationRewardTransferSnapshot, bool) {
	snapshot, err := k.DelegationRewardTransferSnapshot.Get(ctx)
	if err != nil {
		return types.DelegationRewardTransferSnapshot{}, false
	}
	return snapshot, true
}

func (k Keeper) GetDelegationRewardTransfersForEpoch(ctx context.Context, epochIndex uint64) ([]*types.DelegationRewardTransfer, error) {
	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx)
	if !found || snapshot.EpochIndex != epochIndex {
		return nil, nil
	}
	transfers := make([]*types.DelegationRewardTransfer, len(snapshot.Transfers))
	copy(transfers, snapshot.Transfers)
	return transfers, nil
}
