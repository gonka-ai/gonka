package v0_2_16

import (
	"context"
	"encoding/json"
	"fmt"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// UpgradeInfo is extra JSON in the software-upgrade proposal's `info` /
// --upgrade-info field. Cosmovisor already stores binaries/api_binaries in the
// same object; unknown keys are ignored.
//
// Omitted or empty enabled_fee_groups keeps coins off (extra gas still runs).
// Enable later with MsgUpdateParams. To charge at upgrade height, add the same
// field the param uses:
//
//	"enabled_fee_groups": ["epoch"],
//	"min_gas_prices": {"epoch": 10}
type UpgradeInfo struct {
	EnabledFeeGroups []string          `json:"enabled_fee_groups"`
	MinGasPrices     map[string]uint64 `json:"min_gas_prices"`
}

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

		// Apply after RunMigrations: inference module 14 forces enabled=[].
		if err := applyFeeGroupUpgradeInfo(ctx, k, plan.Info); err != nil {
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

	// the block defaults are tuned for short epochs, so raise them to the
	// minimums this chain's epoch length requires before training can be enabled
	if epoch := params.EpochParams; epoch != nil && epoch.EpochLength > 0 {
		if params.TrainingParams.OptInTtlBlocks < epoch.EpochLength {
			params.TrainingParams.OptInTtlBlocks = epoch.EpochLength
		}
		minRetention := 2*epoch.EpochLength + params.TrainingParams.ReleaseBufferBlocks
		if params.TrainingParams.SettledShardRetentionBlocks < minRetention {
			params.TrainingParams.SettledShardRetentionBlocks = minRetention
		}
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("backfilled training param defaults", types.Upgrades,
		"filled_new_fields", changed,
		"opt_in_ttl_blocks", params.TrainingParams.OptInTtlBlocks,
		"release_buffer_blocks", params.TrainingParams.ReleaseBufferBlocks,
		"settled_shard_retention_blocks", params.TrainingParams.SettledShardRetentionBlocks,
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
		kv, err := iter.KeyValue()
		if err != nil {
			return fmt.Errorf("read training opt-in: %w", err)
		}
		if kv.Value == 0 {
			stale = append(stale, kv.Key)
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

func applyFeeGroupUpgradeInfo(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no upgrade info, fee groups stay disabled", types.Upgrades)
		return nil
	}

	var info UpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return fmt.Errorf("unmarshal v0.2.16 upgrade info: %w", err)
	}
	if len(info.EnabledFeeGroups) == 0 {
		k.LogInfo("enabled_fee_groups empty, fee groups stay disabled", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("get inference params: %w", err)
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}

	for _, name := range info.EnabledFeeGroups {
		price, ok := info.MinGasPrices[name]
		if !ok || price == 0 {
			return fmt.Errorf("enabled fee group %q requires min_gas_prices[%q] > 0", name, name)
		}
		group := params.FeeParams.GroupByName(name)
		if group == nil {
			return fmt.Errorf("enabled fee group %q has no groups[] entry", name)
		}
		group.MinGasPrice = price
	}
	for name := range info.MinGasPrices {
		found := false
		for _, enabled := range info.EnabledFeeGroups {
			if enabled == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("min_gas_prices[%q] is not in enabled_fee_groups", name)
		}
	}

	params.FeeParams.EnabledFeeGroups = info.EnabledFeeGroups
	params.FeeParams.MinGasPriceNgonka = 0
	if err := params.FeeParams.Validate(); err != nil {
		return fmt.Errorf("fee params after applying upgrade info: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("enabled fee groups from upgrade info", types.Upgrades,
		"enabled_fee_groups", info.EnabledFeeGroups,
		"min_gas_prices", info.MinGasPrices)
	return nil
}
