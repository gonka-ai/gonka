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

// The reward snapshot is intentionally a singleton. Settlement is one-shot:
// SettleAccounts runs only during the epoch transition for the effective epoch,
// and failed settlement is not recomputed later. The epoch guard prevents callers
// from applying the currently stored snapshot to a different epoch; historical
// reward estimates are available only while that epoch's snapshot is stored.
func (k Keeper) GetDelegationRewardTransfersForEpoch(ctx context.Context, epochIndex uint64) ([]*types.DelegationRewardTransfer, error) {
	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx)
	if !found || snapshot.EpochIndex != epochIndex {
		return nil, nil
	}
	transfers := make([]*types.DelegationRewardTransfer, len(snapshot.Transfers))
	copy(transfers, snapshot.Transfers)
	return transfers, nil
}

func (k Keeper) GetDelegationRewardPenaltiesForEpoch(ctx context.Context, epochIndex uint64) ([]*types.DelegationRewardPenalty, error) {
	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx)
	if !found || snapshot.EpochIndex != epochIndex {
		return nil, nil
	}
	penalties := make([]*types.DelegationRewardPenalty, len(snapshot.Penalties))
	copy(penalties, snapshot.Penalties)
	return penalties, nil
}
