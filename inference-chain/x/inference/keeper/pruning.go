package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	LookbackMultiplier = int64(5)

	// DefaultPruningMaxPerBlock is the fallback when InferencePruningMax / PocPruningMax
	// are zero (proto default). This prevents pruning from being silently disabled.
	DefaultPruningMaxPerBlock = int64(5000)
)

// effectivePruningMax returns max if it is positive, otherwise the default.
// effectivePruningMax guards against configurations where the pruning max was
// never set (proto default 0), which would otherwise disable pruning entirely.
func effectivePruningMax(max int64) int64 {
	if max > 0 {
		return max
	}
	return DefaultPruningMaxPerBlock
}

// EffectivePruningMaxExported is an exported wrapper for testing.
func EffectivePruningMaxExported(max int64) int64 {
	return effectivePruningMax(max)
}

func (k Keeper) Prune(ctx context.Context, currentEpochIndex int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	// --- Original pruners (Inferences, PoCBatches, PoCValidations v1) ---

	err = k.GetInferencePruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		k.LogError("Inference pruning failed", types.Pruning, "error", err)
	}
	err = k.GetPoCBatchesPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		k.LogError("PoC batches pruning failed", types.Pruning, "error", err)
	}
	err = k.GetPoCValidationsPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		k.LogError("PoC validations pruning failed", types.Pruning, "error", err)
	}
	err = k.GetEpochGroupValidationPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		k.LogError("EpochGroupValidation pruning failed", types.Pruning, "error", err)
	}
	err = k.GetSubnetPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		k.LogError("Subnet pruning failed", types.Pruning, "error", err)
	}

	// --- Additional epoch-keyed data cleanup ---
	// These collections grow unboundedly per epoch but were never pruned,
	// causing application.db to grow without limit.
	// We use InferencePruningEpochThreshold (default=2) rather than
	// PocDataPruningEpochThreshold (default=1) because ClaimRewards reads
	// EpochGroupData for the previous epoch (currentEpoch-1). With threshold=1
	// that data would be pruned before claims are processed.
	pruningThreshold := params.EpochParams.InferencePruningEpochThreshold
	pruneMax := effectivePruningMax(params.EpochParams.PocPruningMax)
	err = k.pruneEpochKeyedData(ctx, currentEpochIndex, pruningThreshold, pruneMax)
	if err != nil {
		k.LogError("Epoch-keyed data pruning failed", types.Pruning, "error", err)
	}

	return nil
}

func (k Keeper) GetEpochGroupValidationPruner(params types.Params) Pruner[collections.Triple[uint64, string, string], collections.NoValue] {
	return Pruner[collections.Triple[uint64, string, string], collections.NoValue]{
		Threshold:  params.EpochParams.InferencePruningEpochThreshold,
		PruningMax: effectivePruningMax(params.EpochParams.InferencePruningMax),
		List:       collections.Map[collections.Triple[uint64, string, string], collections.NoValue](k.EpochGroupValidationEntry),
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[uint64, string, string]] {
			return collections.NewPrefixedTripleRange[uint64, string, string](uint64(epochIndex))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.EpochGroupValidationsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.EpochGroupValidationsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[uint64, string, string]) error {
			return k.EpochGroupValidationEntry.Remove(ctx, key)
		},
		Logger: k,
	}
}

// pruneEpochKeyedData cleans up collections that are keyed by epoch index or
// pocStageStartBlockHeight but had no pruning logic before. Each collection
// is cleaned up independently to avoid one failure blocking the rest.
func (k Keeper) pruneEpochKeyedData(ctx context.Context, currentEpochIndex int64, threshold uint64, maxPerCollection int64) error {
	endEpoch := currentEpochIndex - int64(threshold)
	if endEpoch < 1 {
		return nil
	}

	// We prune data for epochs [1, endEpoch]. To bound work per block, we
	// limit each collection to maxPerCollection removals.

	// PoC Validations V2 (keyed by pocStageStartBlockHeight, participant, validator)
	k.prunePoCV2Validations(ctx, endEpoch, maxPerCollection)

	// PoC V2 Store Commits (keyed by pocStageStartBlockHeight, participant)
	k.prunePoCV2StoreCommits(ctx, endEpoch, maxPerCollection)

	// ML Node Weight Distributions (keyed by pocStageStartBlockHeight, participant)
	k.pruneMLNodeWeightDistributions(ctx, endEpoch, maxPerCollection)

	// PoC Validation Snapshots (keyed by pocStageStartHeight)
	k.prunePoCValidationSnapshots(ctx, endEpoch)

	// Epoch-indexed collections: EpochGroupData,
	// EpochPerformanceSummaries, ConfirmationPoCEvents, RandomSeeds
	// Note: EpochGroupValidations are handled by GetEpochGroupValidationPruner
	// (which prunes the migrated EpochGroupValidationEntry store).
	k.pruneEpochGroupData(ctx, endEpoch, maxPerCollection)
	k.pruneEpochPerformanceSummaries(ctx, endEpoch, maxPerCollection)
	k.pruneConfirmationPoCEvents(ctx, endEpoch, maxPerCollection)
	k.pruneRandomSeeds(ctx, endEpoch, maxPerCollection)

	return nil
}

// prunePoCV2Validations removes PoC v2 validations for old epochs.
func (k Keeper) prunePoCV2Validations(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := int64(1); epochIdx <= endEpoch && removed < maxRemovals; epochIdx++ {
		epoch, found := k.GetEpoch(ctx, uint64(epochIdx))
		if !found || epoch == nil {
			continue
		}
		pocHeight := epoch.PocStartBlockHeight
		iter, err := k.PoCValidationsV2.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, sdk.AccAddress](pocHeight))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.PoCValidationsV2.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned PoC v2 validations", types.Pruning, "removed", removed)
	}
}

// prunePoCV2StoreCommits removes PoC v2 store commits for old epochs.
func (k Keeper) prunePoCV2StoreCommits(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := int64(1); epochIdx <= endEpoch && removed < maxRemovals; epochIdx++ {
		epoch, found := k.GetEpoch(ctx, uint64(epochIdx))
		if !found || epoch == nil {
			continue
		}
		pocHeight := epoch.PocStartBlockHeight
		iter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](pocHeight))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.PoCV2StoreCommits.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned PoC v2 store commits", types.Pruning, "removed", removed)
	}
}

// pruneMLNodeWeightDistributions removes weight distributions for old epochs.
func (k Keeper) pruneMLNodeWeightDistributions(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := int64(1); epochIdx <= endEpoch && removed < maxRemovals; epochIdx++ {
		epoch, found := k.GetEpoch(ctx, uint64(epochIdx))
		if !found || epoch == nil {
			continue
		}
		pocHeight := epoch.PocStartBlockHeight
		iter, err := k.MLNodeWeightDistributions.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](pocHeight))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.MLNodeWeightDistributions.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned ML node weight distributions", types.Pruning, "removed", removed)
	}
}

// prunePoCValidationSnapshots removes validation snapshots for old epochs.
// These are keyed by a single int64 (pocStageStartHeight), so we iterate
// through all epochs up to endEpoch and remove their snapshots.
func (k Keeper) prunePoCValidationSnapshots(ctx context.Context, endEpoch int64) {
	removed := int64(0)
	for epochIdx := int64(1); epochIdx <= endEpoch; epochIdx++ {
		epoch, found := k.GetEpoch(ctx, uint64(epochIdx))
		if !found || epoch == nil {
			continue
		}
		err := k.PoCValidationSnapshots.Remove(ctx, epoch.PocStartBlockHeight)
		if err == nil {
			removed++
		}
	}
	if removed > 0 {
		k.LogInfo("Pruned PoC validation snapshots", types.Pruning, "removed", removed)
	}
}

// pruneEpochGroupData removes epoch group data entries for old epochs.
func (k Keeper) pruneEpochGroupData(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := uint64(1); epochIdx <= uint64(endEpoch) && removed < maxRemovals; epochIdx++ {
		iter, err := k.EpochGroupDataMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](epochIdx))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.EpochGroupDataMap.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned epoch group data", types.Pruning, "removed", removed)
	}
}

// pruneEpochPerformanceSummaries removes performance summaries for old epochs.
// Note: this collection is keyed by (participant, epochIndex), so we need to
// iterate all entries and check the epoch index in the key.
func (k Keeper) pruneEpochPerformanceSummaries(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	iter, err := k.EpochPerformanceSummaries.Iterate(ctx, nil)
	if err != nil {
		return
	}
	defer iter.Close()

	var toRemove []collections.Pair[sdk.AccAddress, uint64]
	for ; iter.Valid() && int64(len(toRemove)) < maxRemovals; iter.Next() {
		key, err := iter.Key()
		if err != nil {
			break
		}
		epochIdx := key.K2()
		if epochIdx > 0 && int64(epochIdx) <= endEpoch {
			toRemove = append(toRemove, key)
		}
	}

	for _, key := range toRemove {
		_ = k.EpochPerformanceSummaries.Remove(ctx, key)
		removed++
	}
	if removed > 0 {
		k.LogInfo("Pruned epoch performance summaries", types.Pruning, "removed", removed)
	}
}

// pruneConfirmationPoCEvents removes confirmation PoC events for old epochs.
func (k Keeper) pruneConfirmationPoCEvents(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := uint64(1); epochIdx <= uint64(endEpoch) && removed < maxRemovals; epochIdx++ {
		iter, err := k.ConfirmationPoCEvents.Iterate(ctx, collections.NewPrefixedPairRange[uint64, uint64](epochIdx))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.ConfirmationPoCEvents.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned confirmation PoC events", types.Pruning, "removed", removed)
	}
}

// pruneRandomSeeds removes random seeds for old epochs.
func (k Keeper) pruneRandomSeeds(ctx context.Context, endEpoch int64, maxRemovals int64) {
	removed := int64(0)
	for epochIdx := uint64(1); epochIdx <= uint64(endEpoch) && removed < maxRemovals; epochIdx++ {
		iter, err := k.RandomSeeds.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIdx))
		if err != nil {
			continue
		}
		for ; iter.Valid() && removed < maxRemovals; iter.Next() {
			key, err := iter.Key()
			if err != nil {
				break
			}
			_ = k.RandomSeeds.Remove(ctx, key)
			removed++
		}
		iter.Close()
	}
	if removed > 0 {
		k.LogInfo("Pruned random seeds", types.Pruning, "removed", removed)
	}
}

func (k Keeper) GetInferencePruner(params types.Params) Pruner[collections.Pair[int64, string], collections.NoValue] {
	return Pruner[collections.Pair[int64, string], collections.NoValue]{
		Threshold:  params.EpochParams.InferencePruningEpochThreshold,
		PruningMax: effectivePruningMax(params.EpochParams.InferencePruningMax),
		List:       k.InferencesToPrune,
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[int64, string]] {
			return collections.NewPrefixedPairRange[int64, string](epoch)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.InferencePrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.InferencePrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[int64, string]) error {
			// Check if the inference is still in an active state; if so, skip
			// removing the inference itself but still remove it from the prune list
			// so we don't re-evaluate it every block.
			inf, found := k.GetInference(ctx, key.K2())
			if found && (inf.Status == types.InferenceStatus_STARTED || inf.Status == types.InferenceStatus_VOTING) {
				return k.InferencesToPrune.Remove(ctx, key)
			}
			err := k.Inferences.Remove(ctx, key.K2())
			if err != nil {
				return err
			}
			return k.InferencesToPrune.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetPoCBatchesPruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, string], types.PoCBatch] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, string], types.PoCBatch]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: effectivePruningMax(params.EpochParams.PocPruningMax),
		List:       k.PoCBatches,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, string]] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				// Impossible as far as I know.
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](0)
			}
			return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.PocBatchesPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.PocBatchesPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[int64, sdk.AccAddress, string]) error {
			return k.PoCBatches.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetSubnetPruner(params types.Params) Pruner[collections.Pair[uint64, uint64], collections.NoValue] {
	return Pruner[collections.Pair[uint64, uint64], collections.NoValue]{
		Threshold:  SubnetPruningThreshold,
		PruningMax: SubnetPruningMax,
		List:       k.SubnetEscrowsByEpoch,
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[uint64, uint64]] {
			return collections.NewPrefixedPairRange[uint64, uint64](uint64(epoch))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.SubnetPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.SubnetPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[uint64, uint64]) error {
			epochIndex := key.K1()
			escrowID := key.K2()

			escrow, found := k.GetSubnetEscrow(ctx, escrowID)
			if found && !escrow.Settled {
				if err := k.distributeUnsettledEscrow(ctx, escrow); err != nil {
					k.LogError("failed to distribute unsettled escrow", types.Pruning,
						"escrow_id", escrowID, "error", err)
				}
			}

			// Delete escrow and index entry
			if err := k.SubnetEscrows.Remove(ctx, escrowID); err != nil {
				k.LogError("failed to remove subnet escrow", types.Pruning, "escrow_id", escrowID, "error", err)
			}
			if err := k.SubnetEscrowsByEpoch.Remove(ctx, collections.Join(epochIndex, escrowID)); err != nil {
				k.LogError("failed to remove subnet escrow index", types.Pruning, "escrow_id", escrowID, "error", err)
			}
			return nil
		},
		PostPruneEpoch: func(ctx context.Context, epoch int64) error {
			epochIndex := uint64(epoch)
			// Clear SubnetHostEpochStats for this epoch
			statsRng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex)
			err := k.SubnetHostEpochStatsMap.Clear(ctx, statsRng)
			if err != nil {
				k.LogError("failed to clear subnet host epoch stats", types.Pruning, "epoch", epochIndex, "error", err)
			}
			// Delete epoch count
			err = k.SubnetEscrowEpochCount.Remove(ctx, epochIndex)
			if err != nil {
				k.LogError("failed to remove subnet escrow epoch count", types.Pruning, "epoch", epochIndex, "error", err)
			}
			return nil
		},
		Logger: k,
	}
}

func (k Keeper) GetPoCValidationsPruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: effectivePruningMax(params.EpochParams.PocPruningMax),
		List:       k.PoCValidations,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress]] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				// Impossible?
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, sdk.AccAddress](0)
			}
			return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, sdk.AccAddress](epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.PocValidationsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.PocValidationsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[int64, sdk.AccAddress, sdk.AccAddress]) error {
			return k.PoCValidations.Remove(ctx, key)
		},
		Logger: k,
	}
}

type Pruner[K any, V any] struct {
	Threshold      uint64
	PruningMax     int64
	List           collections.Map[K, V]
	Ranger         func(ctx context.Context, epoch int64) collections.Ranger[K]
	Logger         types.InferenceLogger
	GetLastPruned  func(pruningState types.PruningState) int64
	SetLastPruned  func(pruningState *types.PruningState, epoch int64)
	Remover        func(ctx context.Context, key K) error
	PostPruneEpoch func(ctx context.Context, epoch int64) error
}

func (p Pruner[K, V]) PruneEpoch(ctx context.Context, currentEpochIndex int64, prunesLeft int64) (int64, error) {
	prunedCount := int64(0)
	iter, err := p.List.Iterate(ctx, p.Ranger(ctx, currentEpochIndex))
	if err != nil {
		p.Logger.LogError("Failed to iterate over list to prune", types.Pruning, "error", err, "list", p.List.GetName())
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		pk, err := iter.Key()
		if err != nil {
			p.Logger.LogError("Failed to get key from iterator", types.Pruning, "error", err, "list", p.List.GetName())
			return prunedCount, err
		}
		err = p.Remover(ctx, pk)
		if err != nil {
			p.Logger.LogError("Failed to remove from list to prune", types.Pruning, "error", err, "list", p.List.GetName())
			return prunedCount, err
		}
		prunedCount++
		if prunedCount >= prunesLeft {
			return prunedCount, nil
		}
	}
	return prunedCount, nil
}

func (p Pruner[K, V]) Prune(ctx context.Context, k Keeper, currentEpochIndex int64) error {
	pruningState, err := k.PruningState.Get(ctx)
	if err != nil {
		p.Logger.LogError("Failed to get pruning state", types.Pruning,
			"error", err,
			"list", p.List.GetName(),
		)
		return err
	}
	startEpoch, endEpoch := getEpochsToPrune(p.Threshold, currentEpochIndex, p.GetLastPruned(pruningState))
	if startEpoch > endEpoch {
		p.Logger.LogDebug("No epochs to prune", types.Pruning)
		return nil
	}
	p.Logger.LogInfo("Starting pruning", types.Pruning,
		"start_epoch", startEpoch,
		"end_epoch", endEpoch,
		"threshold", p.Threshold,
		"pruning_max", p.PruningMax,
		"list", p.List.GetName())
	totalPruned := int64(0)
	for epoch := startEpoch; epoch <= endEpoch; epoch++ {
		prunesLeft := p.PruningMax - totalPruned
		if prunesLeft <= 0 {
			p.Logger.LogInfo("Pruning max reached, deferring remaining epochs", types.Pruning,
				"epoch", epoch, "total_pruned", totalPruned, "list", p.List.GetName())
			break
		}
		prunedForEpoch, err := p.PruneEpoch(ctx, epoch, prunesLeft)
		if err != nil {
			p.Logger.LogError("Failed to prune epoch", types.Pruning,
				"epoch", epoch,
				"error", err,
			)
			continue
		}
		totalPruned += prunedForEpoch
		if prunedForEpoch == 0 {
			p.Logger.LogInfo("Pruning epoch complete", types.Pruning, "epoch", epoch, "list", p.List.GetName())

			if p.PostPruneEpoch != nil {
				if err := p.PostPruneEpoch(ctx, epoch); err != nil {
					p.Logger.LogError("Failed post-prune epoch", types.Pruning,
						"epoch", epoch,
						"error", err,
					)
				}
			}

			currentPruningState, err := k.PruningState.Get(ctx)
			if err != nil {
				p.Logger.LogError("Failed to get pruning state", types.Pruning,
					"epoch", epoch,
					"error", err,
					"list", p.List.GetName(),
				)
				return err
			}
			if p.GetLastPruned(currentPruningState) < epoch {
				p.SetLastPruned(&currentPruningState, epoch)
				err = k.PruningState.Set(ctx, currentPruningState)
				if err != nil {
					p.Logger.LogError("Failed to mark epoch complete", types.Pruning,
						"epoch", epoch,
						"error", err,
						"list", p.List.GetName(),
					)
				}
			}
		} else {
			p.Logger.LogInfo("Items pruned for epoch", types.Pruning, "epoch", epoch, "pruned", prunedForEpoch, "list", p.List.GetName())
		}
	}
	if totalPruned > 0 {
		p.Logger.LogInfo("Pruning cycle complete", types.Pruning,
			"total_pruned", totalPruned, "list", p.List.GetName())
	}
	return nil
}

func getEpochsToPrune(pruningThreshold uint64, currentEpochIndex int64, lastPrunedEpoch int64) (int64, int64) {
	startEpoch := lastPrunedEpoch + 1
	endEpoch := currentEpochIndex - int64(pruningThreshold)
	if endEpoch < 0 {
		endEpoch = 0
	}
	return startEpoch, endEpoch
}
