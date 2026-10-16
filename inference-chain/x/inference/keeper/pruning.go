package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	LookbackMultiplier               = int64(5)
	ClaimRecipientPruningThreshold   = uint64(5)
	ClaimRecipientPruningMaxPerBlock = int64(1000)
	// Seeds and CPoC events are read only for the current or upcoming epoch.
	EpochRecordPruningThreshold   = uint64(5)
	EpochRecordPruningMaxPerBlock = int64(1000)
	// PruneWorkPerBlock caps removals of all pruners in one EndBlock. Reads are not
	// charged; the epoch-0 pass bounds its reads separately.
	PruneWorkPerBlock = int64(5000)
	// Epoch-0 inference pass: removals and keys read per block.
	EpochZeroInferencePruningMaxPerBlock = 1000
	EpochZeroInferenceScanMaxPerBlock    = 4000
)

// Prune runs every pruner within one shared PruneWorkPerBlock budget. The first pruner
// rotates with the height, so each pruner runs first at least once per len(pruners) blocks
// and then removes up to its PruningMax; in other blocks a backlog ahead of it may take the rest.
// A failing pruner stops only itself; the errors are joined.
func (k Keeper) Prune(ctx context.Context, currentEpochIndex int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	pruners := []budgetedPruner{
		k.GetInferencePruner(params),
		k.GetPoCBatchesPruner(params),
		k.GetPoCValidationsPruner(params),
		k.GetPoCValidationsV2Pruner(params),
		k.GetPoCV2StoreCommitPruner(params),
		k.GetMLNodeWeightDistributionPruner(params),
		k.GetPoCValidationSnapshotPruner(params),
		k.GetEpochGroupValidationPruner(params),
		k.GetDevshardPruner(params),
		k.GetClaimRecipientPruner(params),
		k.GetEpochZeroInferencePruner(params),
		k.GetRandomSeedPruner(),
		k.GetConfirmationPoCEventPruner(),
	}
	budget := PruneWorkPerBlock
	first := int(uint64(sdk.UnwrapSDKContext(ctx).BlockHeight()) % uint64(len(pruners)))
	var errs []error
	for i := range pruners {
		p := pruners[(first+i)%len(pruners)]
		if err := p.prune(ctx, k, currentEpochIndex, &budget); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// epochZeroInferencePruner removes finished or expired inferences left with EpochId 0
// (started or finished without the other half), which the indexed pruner never reaches.
// It runs once epoch 0 is past the inference threshold, a bounded slice per block.
type epochZeroInferencePruner struct {
	params types.Params
	remove func(ctx context.Context, id string) error // nil: k.Inferences.Remove
}

func (k Keeper) GetEpochZeroInferencePruner(params types.Params) epochZeroInferencePruner {
	return epochZeroInferencePruner{params: params}
}

func (p epochZeroInferencePruner) prune(ctx context.Context, k Keeper, currentEpochIndex int64, budget *int64) error {
	if currentEpochIndex < int64(p.params.EpochParams.InferencePruningEpochThreshold) {
		return nil
	}
	limit := int64(EpochZeroInferencePruningMaxPerBlock)
	if budget != nil {
		limit = min(limit, *budget)
		if limit <= 0 {
			return nil
		}
	}
	state, err := k.PruningState.Get(ctx)
	if err != nil {
		return err
	}
	if state.EpochZeroInferencesPruned {
		return nil
	}
	// Early records were indexed under epoch 0, which the indexed pruner never visits.
	dropped, err := k.pruneEpochZeroIndex(ctx, limit)
	if budget != nil {
		*budget -= dropped
	}
	if err != nil || dropped >= limit {
		return err
	}
	limit -= dropped
	rng := new(collections.Range[string])
	if state.EpochZeroInferencesCursor != "" {
		rng = rng.StartExclusive(state.EpochZeroInferencesCursor)
	}
	iter, err := k.Inferences.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	var toRemove []string
	cursor, scanned := state.EpochZeroInferencesCursor, 0
	for ; iter.Valid() && scanned < EpochZeroInferenceScanMaxPerBlock && int64(len(toRemove)) < limit; iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			iter.Close()
			return err
		}
		scanned++
		cursor = kv.Key
		status := kv.Value.Status
		if kv.Value.EpochId == 0 && status != types.InferenceStatus_STARTED && status != types.InferenceStatus_VOTING {
			toRemove = append(toRemove, kv.Key)
		}
	}
	done := !iter.Valid()
	iter.Close()
	if budget != nil {
		*budget -= int64(len(toRemove))
	}
	remove := p.remove
	if remove == nil {
		remove = k.Inferences.Remove
	}
	// The cursor is saved only after every removal succeeded; on error it stays put
	// and the next block scans the same range again.
	for _, id := range toRemove {
		if err := remove(ctx, id); err != nil {
			return err
		}
	}
	state.EpochZeroInferencesCursor = cursor
	state.EpochZeroInferencesPruned = done
	if done {
		state.EpochZeroInferencesCursor = ""
		k.LogInfo("Epoch-0 inference pruning complete", types.Pruning)
	}
	return k.PruningState.Set(ctx, state)
}

// pruneEpochZeroIndex removes up to max InferencesToPrune keys of epoch 0.
func (k Keeper) pruneEpochZeroIndex(ctx context.Context, max int64) (int64, error) {
	iter, err := k.InferencesToPrune.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](0))
	if err != nil {
		return 0, err
	}
	var keys []collections.Pair[int64, string]
	for ; iter.Valid() && int64(len(keys)) < max; iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return 0, err
		}
		keys = append(keys, key)
	}
	iter.Close()
	for i, key := range keys {
		if err := k.InferencesToPrune.Remove(ctx, key); err != nil {
			return int64(i), err
		}
	}
	return int64(len(keys)), nil
}

// pocStagePruneBound returns the first PoC stage kept when pruning epochID: the next
// epoch's PoC start. Confirmation PoC data is keyed by trigger height, which lies between
// the two regular stages, so pruning by the epoch's own stage alone never removes it.
// Without the next epoch only the epoch's own stage is pruned, as before.
func (k Keeper) pocStagePruneBound(ctx context.Context, epochID int64) (stage int64, before bool) {
	next, found := k.GetEpoch(ctx, uint64(epochID+1))
	if found && next.PocStartBlockHeight > 0 {
		return next.PocStartBlockHeight, true
	}
	epoch, found := k.GetEpoch(ctx, uint64(epochID))
	if !found {
		k.LogError("Failed to get epoch", types.Pruning, "epoch", epochID)
		return 0, false
	}
	return epoch.PocStartBlockHeight, false
}

// pocStageRanger covers every stage below stage when before is set, otherwise stage only.
func pocStageRanger[K2, K3 any](stage int64, before bool) collections.Ranger[collections.Triple[int64, K2, K3]] {
	if before {
		return collections.NewPrefixUntilTripleRange[int64, K2, K3](stage - 1)
	}
	return collections.NewPrefixedTripleRange[int64, K2, K3](stage)
}

func (k Keeper) GetPoCValidationsV2Pruner(params types.Params) Pruner[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]], types.PoCValidationV2] {
	return Pruner[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]], types.PoCValidationV2]{
		Threshold:  params.PocParams.PocDataPruningEpochThreshold,
		PruningMax: params.EpochParams.PocPruningMax,
		List:       k.PoCValidationsV2,
		Ranger: func(ctx context.Context, epochIndex int64) collections.Ranger[collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]]] {
			return pocStageRanger[sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](k.pocStagePruneBound(ctx, epochIndex))
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
			return pocStageRanger[sdk.AccAddress, string](k.pocStagePruneBound(ctx, epochIndex))
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
			return pocStageRanger[sdk.AccAddress, string](k.pocStagePruneBound(ctx, epochIndex))
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
			inference, found := k.GetInference(ctx, key.K2())
			if found && (inference.Status == types.InferenceStatus_VOTING || inference.Status == types.InferenceStatus_STARTED) {
				retryEpoch, retryFound := k.GetEffectiveEpochIndex(ctx)
				if !retryFound {
					return fmt.Errorf("cannot defer pruning inference %q: effective epoch not found", key.K2())
				}
				if int64(retryEpoch) <= key.K1() {
					return fmt.Errorf("cannot defer pruning inference %q from epoch %d to epoch %d", key.K2(), key.K1(), retryEpoch)
				}

				// Move active inferences forward so the completed epoch can advance
				// while the inference remains discoverable by a later pruning pass.
				if err := k.InferencesToPrune.Set(ctx, collections.Join(int64(retryEpoch), key.K2()), collections.NoValue{}); err != nil {
					return err
				}
				return k.InferencesToPrune.Remove(ctx, key)
			}

			if err := k.Inferences.Remove(ctx, key.K2()); err != nil {
				return err
			}
			// A status update can re-add the inference under its original epoch
			// after an active record was deferred. Remove that stale index too.
			if found && int64(inference.EpochId) != key.K1() {
				if err := k.InferencesToPrune.Remove(ctx, collections.Join(int64(inference.EpochId), key.K2())); err != nil {
					return err
				}
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
			return pocStageRanger[sdk.AccAddress, string](k.pocStagePruneBound(ctx, epochIndex))
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
			if err := k.DevshardSettledEscrows.Remove(ctx, escrowID); err != nil {
				k.LogError("failed to remove devshard settled mark", types.Pruning, "escrow_id", escrowID, "error", err)
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

func (k Keeper) GetRandomSeedPruner() Pruner[collections.Pair[uint64, sdk.AccAddress], types.RandomSeed] {
	return Pruner[collections.Pair[uint64, sdk.AccAddress], types.RandomSeed]{
		Threshold:  EpochRecordPruningThreshold,
		PruningMax: EpochRecordPruningMaxPerBlock,
		List:       k.RandomSeeds,
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[uint64, sdk.AccAddress]] {
			return collections.NewPrefixedPairRange[uint64, sdk.AccAddress](uint64(epoch))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.RandomSeedsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.RandomSeedsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[uint64, sdk.AccAddress]) error {
			return k.RandomSeeds.Remove(ctx, key)
		},
		Logger: k,
	}
}

func (k Keeper) GetConfirmationPoCEventPruner() Pruner[collections.Pair[uint64, uint64], types.ConfirmationPoCEvent] {
	return Pruner[collections.Pair[uint64, uint64], types.ConfirmationPoCEvent]{
		Threshold:  EpochRecordPruningThreshold,
		PruningMax: EpochRecordPruningMaxPerBlock,
		List:       k.ConfirmationPoCEvents,
		Ranger: func(ctx context.Context, epoch int64) collections.Ranger[collections.Pair[uint64, uint64]] {
			return collections.NewPrefixedPairRange[uint64, uint64](uint64(epoch))
		},
		GetLastPruned: func(state types.PruningState) int64 {
			return state.ConfirmationPocEventsPrunedEpoch
		},
		SetLastPruned: func(state *types.PruningState, epoch int64) {
			state.ConfirmationPocEventsPrunedEpoch = epoch
		},
		Remover: func(ctx context.Context, key collections.Pair[uint64, uint64]) error {
			return k.ConfirmationPoCEvents.Remove(ctx, key)
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
			return pocStageRanger[sdk.AccAddress, sdk.AccAddress](k.pocStagePruneBound(ctx, epochIndex))
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

type budgetedPruner interface {
	prune(ctx context.Context, k Keeper, currentEpochIndex int64, budget *int64) error
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
	if prunesLeft <= 0 {
		return 0, nil
	}
	p.Logger.LogDebug("PruneEpoch called", types.Pruning, "epoch", currentEpochIndex, "prunesLeft", prunesLeft, "list", p.List.GetName())
	prunedCount := int64(0)
	iter, err := p.List.Iterate(ctx, p.Ranger(ctx, currentEpochIndex))
	if err != nil {
		p.Logger.LogError("Failed to iterate over list to prune", types.Pruning, "error", err, "list", p.List.GetName())
		return 0, err
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

// Prune runs this pruner alone, bounded by its own PruningMax.
func (p Pruner[K, V]) Prune(ctx context.Context, k Keeper, currentEpochIndex int64) error {
	return p.prune(ctx, k, currentEpochIndex, nil)
}

// prune removes at most PruningMax entries; a non-nil budget is shared with other
// pruners and is charged one unit per removal.
func (p Pruner[K, V]) prune(ctx context.Context, k Keeper, currentEpochIndex int64, budget *int64) error {
	if p.PruningMax <= 0 {
		p.Logger.LogError("Skipping pruning with non-positive limit", types.Pruning,
			"max", p.PruningMax,
			"list", p.List.GetName(),
		)
		return nil
	}

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
	limit := p.PruningMax
	if budget != nil {
		limit = min(limit, *budget)
		if limit <= 0 {
			return nil
		}
	}
	prunedCount := int64(0)
	if budget != nil {
		defer func() { *budget -= prunedCount }()
	}
	p.Logger.LogInfo("Starting pruning", types.Pruning,
		"start_epoch", startEpoch,
		"end_epoch", endEpoch,
		"threshold", p.Threshold,
		"list", p.List.GetName())
	for epoch := startEpoch; epoch <= endEpoch; epoch++ {
		prunesLeft := limit - prunedCount
		prunedForEpoch, err := p.PruneEpoch(ctx, epoch, prunesLeft)
		prunedCount += prunedForEpoch
		if err != nil {
			p.Logger.LogError("Failed to prune epoch", types.Pruning,
				"epoch", epoch,
				"error", err,
			)
			return err
		}
		if prunedCount >= limit {
			p.Logger.LogInfo("Reached per-block pruning limit", types.Pruning,
				"pruned", prunedCount,
				"max", limit,
				"list", p.List.GetName(),
			)
			return nil
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
