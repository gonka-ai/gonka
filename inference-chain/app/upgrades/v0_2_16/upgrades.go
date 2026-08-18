package v0_2_16

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := backfillTrainingParamDefaults(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillTrainshardNodeStatuses(ctx, k); err != nil {
			return nil, err
		}
		if err := clearStaleTrainingOptIns(ctx, k); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func backfillTrainingParamDefaults(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.TrainingParams == nil {
		params.TrainingParams = types.DefaultTrainingParams()
	}

	changed := params.TrainingParams.OptInTtlBlocks == 0 || params.TrainingParams.ReleaseBufferBlocks == 0
	if params.TrainingParams.OptInTtlBlocks == 0 {
		params.TrainingParams.OptInTtlBlocks = types.DefaultTrainingOptInTtlBlocks
	}
	if params.TrainingParams.ReleaseBufferBlocks == 0 {
		params.TrainingParams.ReleaseBufferBlocks = types.DefaultTrainingReleaseBufferBlocks
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("backfilled training param defaults", types.Upgrades,
		"filled_new_fields", changed,
		"opt_in_ttl_blocks", params.TrainingParams.OptInTtlBlocks,
		"release_buffer_blocks", params.TrainingParams.ReleaseBufferBlocks,
	)
	return nil
}

func backfillTrainshardNodeStatuses(ctx context.Context, k keeper.Keeper) error {
	var updated []types.Trainshard
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		changed := false
		for _, node := range shard.Nodes {
			if node.Status != types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_UNSPECIFIED {
				continue
			}
			if shard.Status == types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
				node.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
			} else {
				node.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE
				node.ReleasedAtHeight = shard.ClosedAtHeight
				node.ReservedUntilHeight = shard.ClosedAtHeight
			}
			changed = true
		}
		if changed {
			updated = append(updated, shard)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk trainshards for node status backfill: %w", err)
	}

	for _, shard := range updated {
		if err := k.Trainshards.Set(ctx, shard.TrainshardId, shard); err != nil {
			return fmt.Errorf("set trainshard %d during node status backfill: %w", shard.TrainshardId, err)
		}
	}
	k.LogInfo("backfilled trainshard node statuses", types.Upgrades, "updated", len(updated))
	return nil
}

func clearStaleTrainingOptIns(ctx context.Context, k keeper.Keeper) error {
	iter, err := k.TrainingNodeOptIns.Iterate(ctx, nil)
	if err != nil {
		return fmt.Errorf("iterate training opt-ins: %w", err)
	}
	defer iter.Close()

	var stale []collections.Pair[string, string]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return fmt.Errorf("read training opt-in key: %w", err)
		}
		if expiresAt, err := k.TrainingNodeOptIns.Get(ctx, key); err != nil || expiresAt == 0 {
			stale = append(stale, key)
		}
	}

	for _, key := range stale {
		if err := k.TrainingNodeOptIns.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove stale training opt-in %s/%s: %w", key.K1(), key.K2(), err)
		}
	}
	k.LogInfo("cleared stale training opt-ins", types.Upgrades, "removed", len(stale))
	return nil
}
