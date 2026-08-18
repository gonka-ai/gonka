package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) ProcessTrainshardEndBlock(ctx context.Context) {
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	params := k.GetTrainingParams(ctx)
	limit := int(params.MaxExpirationsPerBlock)

	k.expireTrainshards(ctx, height, limit)
	k.clearReturnedReservations(ctx, height, limit)
	k.pruneClosedTrainshards(ctx, height, params.SettledShardRetentionBlocks, limit)
}

func (k Keeper) clearReturnedReservations(ctx context.Context, height int64, limit int) {
	var due []collections.Triple[int64, string, string]
	err := k.TrainshardReleaseIndex.Walk(ctx, nil, func(key collections.Triple[int64, string, string]) (bool, error) {
		if key.K1() > height {
			return true, nil
		}
		due = append(due, key)
		return len(due) >= limit, nil
	})
	if err != nil {
		k.LogError("trainshard release walk failed", types.System, "error", err)
		return
	}

	for _, key := range due {
		if err := k.TrainshardReservations.Remove(ctx, collections.Join(key.K2(), key.K3())); err != nil {
			k.LogError("trainshard reservation release failed, deferring", types.System,
				"participant", key.K2(), "node_id", key.K3(), "error", err)
			// move past this block so repeated failures cannot starve the quota
			k.deferReturnedReservation(ctx, key, height+1)
			continue
		}
		if err := k.TrainshardReleaseIndex.Remove(ctx, key); err != nil {
			k.LogError("trainshard release index cleanup failed", types.System,
				"participant", key.K2(), "node_id", key.K3(), "error", err)
		}
	}
}

func (k Keeper) deferReturnedReservation(ctx context.Context, oldKey collections.Triple[int64, string, string], newHeight int64) {
	if err := k.TrainshardReleaseIndex.Set(ctx, collections.Join3(newHeight, oldKey.K2(), oldKey.K3())); err != nil {
		k.LogError("trainshard release defer reindex failed", types.System,
			"participant", oldKey.K2(), "node_id", oldKey.K3(), "error", err)
		return
	}
	if err := k.TrainshardReleaseIndex.Remove(ctx, oldKey); err != nil {
		k.LogError("trainshard release defer remove failed", types.System,
			"participant", oldKey.K2(), "node_id", oldKey.K3(), "error", err)
	}
}

func (k Keeper) expireTrainshards(ctx context.Context, height int64, limit int) {
	var due []collections.Pair[int64, uint64]
	err := k.TrainshardExpiryIndex.Walk(ctx, nil, func(key collections.Pair[int64, uint64]) (bool, error) {
		if key.K1() > height {
			return true, nil
		}
		due = append(due, key)
		return len(due) >= limit, nil
	})
	if err != nil {
		k.LogError("trainshard expiry walk failed", types.System, "error", err)
		return
	}

	for _, key := range due {
		id := key.K2()
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil || shard.Status != types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
			// drop stale key so it cannot starve the per-block quota
			if remErr := k.TrainshardExpiryIndex.Remove(ctx, key); remErr != nil {
				k.LogError("trainshard expiry stale-key cleanup failed", types.System, "trainshard_id", id, "error", remErr)
			}
			continue
		}
		if err := k.closeTrainshard(ctx, &shard, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED,
			types.TrainshardCloseReason_TRAINSHARD_CLOSE_REASON_TIMEOUT, height); err != nil {
			k.LogError("trainshard expiry close failed, deferring", types.System, "trainshard_id", id, "error", err)
			// move past this block so repeated close failures cannot starve the quota
			k.deferTrainshardExpiry(ctx, key, height+1)
			continue
		}
		// drop the walked key in case defer moved the live key to another height
		if remErr := k.TrainshardExpiryIndex.Remove(ctx, key); remErr != nil {
			k.LogError("trainshard expiry key cleanup failed", types.System, "trainshard_id", id, "error", remErr)
		}
		emitTrainshardEvent(ctx, "trainshard_expired",
			sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", id)),
			sdk.NewAttribute("creator", shard.Creator),
			sdk.NewAttribute("closed_at_height", fmt.Sprintf("%d", height)),
		)
	}
}

func (k Keeper) deferTrainshardExpiry(ctx context.Context, oldKey collections.Pair[int64, uint64], newHeight int64) {
	id := oldKey.K2()
	shard, err := k.Trainshards.Get(ctx, id)
	if err != nil || shard.Status != types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
		if remErr := k.TrainshardExpiryIndex.Remove(ctx, oldKey); remErr != nil {
			k.LogError("trainshard expiry defer cleanup failed", types.System, "trainshard_id", id, "error", remErr)
		}
		return
	}
	if err := k.TrainshardExpiryIndex.Set(ctx, collections.Join(newHeight, id)); err != nil {
		k.LogError("trainshard expiry defer reindex failed", types.System, "trainshard_id", id, "error", err)
		return
	}
	if oldKey.K1() != newHeight {
		if err := k.TrainshardExpiryIndex.Remove(ctx, oldKey); err != nil {
			k.LogError("trainshard expiry defer remove failed", types.System, "trainshard_id", id, "error", err)
		}
	}
}

func (k Keeper) pruneClosedTrainshards(ctx context.Context, height int64, retention int64, limit int) {
	var prunable []collections.Pair[int64, uint64]
	err := k.TrainshardClosedIndex.Walk(ctx, nil, func(key collections.Pair[int64, uint64]) (bool, error) {
		if key.K1()+retention > height {
			return true, nil
		}
		prunable = append(prunable, key)
		return len(prunable) >= limit, nil
	})
	if err != nil {
		k.LogError("trainshard prune walk failed", types.System, "error", err)
		return
	}

	for _, key := range prunable {
		if err := k.Trainshards.Remove(ctx, key.K2()); err != nil {
			k.LogError("trainshard prune remove failed", types.System, "trainshard_id", key.K2(), "error", err)
			continue
		}
		requests := collections.NewPrefixedPairRange[uint64, string](key.K2())
		if err := k.TrainshardAutokickRequest.Clear(ctx, requests); err != nil {
			k.LogError("trainshard prune autokick requests failed", types.System, "trainshard_id", key.K2(), "error", err)
		}
		if err := k.TrainshardClosedIndex.Remove(ctx, key); err != nil {
			k.LogError("trainshard prune index remove failed", types.System, "trainshard_id", key.K2(), "error", err)
		}
	}
}
