package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	LookbackMultiplier               = int64(5)
	ClaimRecipientPruningThreshold   = uint64(5)
	ClaimRecipientPruningMaxPerBlock = int64(1000)
)

func (k Keeper) Prune(ctx context.Context, currentEpochIndex int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	err = k.GetInferencePruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetPoCBatchesPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetPoCValidationsPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetPoCValidationsV2Pruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetPoCV2StoreCommitPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetMLNodeWeightDistributionPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetPoCValidationSnapshotPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetEpochGroupValidationPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetDevshardPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = k.GetClaimRecipientPruner(params).Prune(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	return nil
}

func (k Keeper) GetPoCValidationsV2Pruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]], types.PoCValidationV2] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]], types.PoCValidationV2]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
		List:       k.PoCValidationsV2,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]]] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](0)
			}
			return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.PocValidationsV2PrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.PocValidationsV2PrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]]) error {
			return k.PoCValidationsV2.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetPoCV2StoreCommitPruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, string], types.PoCV2StoreCommit] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, string], types.PoCV2StoreCommit]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
		List:       k.PoCV2StoreCommits,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, string]] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](0)
			}
			return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.PocV2StoreCommitsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.PocV2StoreCommitsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[int64, sdk.AccAddress, string]) error {
			return k.PoCV2StoreCommits.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetMLNodeWeightDistributionPruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, string], types.MLNodeWeightDistribution] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, string], types.MLNodeWeightDistribution]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
		List:       k.MLNodeWeightDistributions,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, string]] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](0)
			}
			return collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.MlnodeWeightDistributionsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.MlnodeWeightDistributionsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Triple[int64, sdk.AccAddress, string]) error {
			return k.MLNodeWeightDistributions.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetPoCValidationSnapshotPruner(params types.Params) Pruner[int64, types.PoCValidationSnapshot] {
	return Pruner[int64, types.PoCValidationSnapshot]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
		List:       k.PoCValidationSnapshots,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[int64] {
			epoch, found := k.GetEpoch(ctx, uint64(epochIndex))
			if !found {
				k.LogError("Failed to get epoch", types.Pruning, "epoch", epochIndex)
				return new(collections.Range[int64]).Prefix(0)
			}
			return new(collections.Range[int64]).Prefix(epoch.PocStartBlockHeight)
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.PocValidationSnapshotsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.PocValidationSnapshotsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key int64) error {
			return k.PoCValidationSnapshots.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetEpochGroupValidationPruner(params types.Params) Pruner[collections.Triple[uint64, string, string], collections.NoValue] {
	return Pruner[collections.Triple[uint64, string, string], collections.NoValue]{
		Threshold:  params.EpochParams.InferencePruningEpochThreshold,
		PruningMax: params.EpochParams.InferencePruningMax,
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

func (k Keeper) GetInferencePruner(params types.Params) Pruner[collections.Pair[int64, string], collections.NoValue] {
	return Pruner[collections.Pair[int64, string], collections.NoValue]{
		Threshold:  params.EpochParams.InferencePruningEpochThreshold,
		PruningMax: params.EpochParams.InferencePruningMax,
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
		PruningMax: params.EpochParams.PocPruningMax,
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

func (k Keeper) GetDevshardPruner(params types.Params) Pruner[collections.Pair[uint64, uint64], collections.NoValue] {
	return Pruner[collections.Pair[uint64, uint64], collections.NoValue]{
		Threshold:  DevshardPruningThreshold,
		PruningMax: DevshardPruningMax,
		List:       k.DevshardEscrowsByEpoch,
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[uint64, uint64]] {
			return collections.NewPrefixedPairRange[uint64, uint64](uint64(epoch))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.DevshardPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.DevshardPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[uint64, uint64]) error {
			epochIndex := key.K1()
			escrowID := key.K2()

			escrow, found := k.GetDevshardEscrow(ctx, escrowID)
			if found && !escrow.Settled {
				if err := k.distributeUnsettledEscrow(ctx, escrow); err != nil {
					k.LogError("failed to distribute unsettled escrow", types.Pruning,
						"escrow_id", escrowID, "error", err)
				}
			}

			// Delete escrow and index entry
			if err := k.DevshardEscrows.Remove(ctx, escrowID); err != nil {
				k.LogError("failed to remove devshard escrow", types.Pruning, "escrow_id", escrowID, "error", err)
			}
			if err := k.DevshardEscrowsByEpoch.Remove(ctx, collections.Join(epochIndex, escrowID)); err != nil {
				k.LogError("failed to remove devshard escrow index", types.Pruning, "escrow_id", escrowID, "error", err)
			}
			return nil
		},
		PostPruneEpoch: func(ctx context.Context, epoch int64) error {
			epochIndex := uint64(epoch)
			// Clear DevshardHostEpochStats for this epoch
			statsRng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex)
			err := k.DevshardHostEpochStatsMap.Clear(ctx, statsRng)
			if err != nil {
				k.LogError("failed to clear devshard host epoch stats", types.Pruning, "epoch", epochIndex, "error", err)
			}
			// Delete epoch count
			err = k.DevshardEscrowEpochCount.Remove(ctx, epochIndex)
			if err != nil {
				k.LogError("failed to remove devshard escrow epoch count", types.Pruning, "epoch", epochIndex, "error", err)
			}
			return nil
		},
		Logger: k,
	}
}

func (k Keeper) GetClaimRecipientPruner(params types.Params) Pruner[collections.Pair[uint64, sdk.AccAddress], collections.NoValue] {
	return Pruner[collections.Pair[uint64, sdk.AccAddress], collections.NoValue]{
		Threshold:  ClaimRecipientPruningThreshold,
		PruningMax: ClaimRecipientPruningMaxPerBlock,
		List:       collections.Map[collections.Pair[uint64, sdk.AccAddress], collections.NoValue](k.ClaimRecipientsByEpoch),
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[uint64, sdk.AccAddress]] {
			return collections.NewPrefixedPairRange[uint64, sdk.AccAddress](uint64(epoch))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.ClaimRecipientsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.ClaimRecipientsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[uint64, sdk.AccAddress]) error {
			return k.RemoveClaimRecipientForEpoch(sdk.UnwrapSDKContext(ctx), key.K2(), key.K1())
		},
		Logger: k,
	}
}

func (k Keeper) GetPoCValidationsPruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
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
		"list", p.List.GetName())
	prunedCount := int64(0)
	for epoch := startEpoch; epoch <= endEpoch; epoch++ {
		prunesLeft := p.PruningMax - prunedCount
		prunedForEpoch, err := p.PruneEpoch(ctx, epoch, prunesLeft)
		if err != nil {
			p.Logger.LogError("Failed to prune epoch", types.Pruning,
				"epoch", epoch,
				"error", err,
			)
			continue
		}
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
	return nil
}

func getEpochsToPrune(pruningThreshold uint64, currentEpochIndex int64, lastPrunedEpoch int64) (int64, int64) {
	startEpoch := lastPrunedEpoch + 1
	//if lastPrunedEpoch+1 > startEpoch {
	//	startEpoch = lastPrunedEpoch + 1
	//}
	endEpoch := currentEpochIndex - int64(pruningThreshold)
	if endEpoch < 0 {
		endEpoch = 0
	}
	return startEpoch, endEpoch
}
