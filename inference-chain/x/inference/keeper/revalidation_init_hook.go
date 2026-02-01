package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"github.com/tidwall/btree"
)

// OnInferenceValidationNeedsRevalidation is called in BeginBlock (of the next block) for each
// inference_validation event from the previous block with needs_revalidation=true. At that time
// the block is finalized and blockHeight/blockHash are known. Dummy implementation; override or extend as needed.
func (k Keeper) OnInferenceValidationNeedsRevalidation(ctx context.Context, inferenceId, validator string, blockHeight int64, blockHash []byte) {
	_ = ctx
	_ = inferenceId
	_ = validator
	_ = blockHeight
	_ = blockHash
	// Dummy: no-op. Replace with real logic (e.g. enqueue revalidation, log, metrics).
}

// BlockRevalidationEventsCollector is an optional extension of BlockRevalidationEventsProvider
// that can discard events for a height (e.g. when that block did not finalize). Used by the
// PostHandler-based collector so we clear the current block at block start and only use
// events from the execution that committed.
type BlockRevalidationEventsCollector interface {
	BlockRevalidationEventsProvider
	ClearEventsForHeight(height int64)
}

// PrepareForBlock is called at the start of each block. It discards revalidation events for
// the current block height (if the provider supports it), invalidates the epoch group
// cache, and evicts old entries from the normalized weighted participants cache (keep last
// NormalizedParticipantsCacheBlocks blocks). Normalized participants for each block are
// computed and cached in the Commit hook when the block hash is known.
func (k *Keeper) PrepareForBlock(ctx context.Context, currentBlockHeight int64) {
	// Discard revalidation events for current height so we only use events from the execution that committed.
	if k.blockRevalidationEventsProvider != nil {
		if c, ok := k.blockRevalidationEventsProvider.(interface{ ClearEventsForHeight(int64) }); ok {
			c.ClearEventsForHeight(currentBlockHeight)
		}
	}
	// Invalidate epoch group cache so we read fresh from store for the new block.
	k.InvalidateEpochGroupCache()

	// Evict blocks older than (current - NormalizedParticipantsCacheBlocks) from the normalized participants cache.
	k.normalizedWeightedParticipants.ClearByHeight(currentBlockHeight - NormalizedParticipantsCacheBlocks)
}

// validationWeightsToParticipantWeights maps ValidationWeights to ParticipantWeight list (MemberAddress -> ConfirmationWeight).
func validationWeightsToParticipantWeights(vws []*types.ValidationWeight) []ParticipantWeight {
	if len(vws) == 0 {
		return nil
	}
	out := make([]ParticipantWeight, 0, len(vws))
	for _, vw := range vws {
		if vw == nil {
			continue
		}
		confirmationWeight := vw.GetConfirmationWeight()
		if confirmationWeight == 0 {
			continue
		}
		out = append(out, ParticipantWeight{
			Address: vw.GetMemberAddress(),
			Weight:  confirmationWeight,
		})
	}
	return out
}

// SetNormalizedParticipantsForCommittedBlock computes normalized weighted participants for the
// committed block from the current effective epoch group data (ValidationWeights -> ConfirmationWeight)
// and adds them to the cache keyed by blockHash. Call once per commit from the Precommiter hook.
func (k *Keeper) SetNormalizedParticipantsForCommittedBlock(ctx context.Context, blockHeight int64, blockHash []byte) {
	effEpoch, ok := k.GetEffectiveEpochIndex(ctx)
	if !ok {
		return
	}
	epochData, found := k.GetEpochGroupData(ctx, effEpoch, "")
	if !found {
		return
	}
	weights := validationWeightsToParticipantWeights(epochData.GetValidationWeights())
	k.normalizedWeightedParticipants.Add(blockHash, blockHeight, weights)
}

// GetNormalizedWeightedParticipants returns the BTree for the given block hash if present in the cache.
// The tree maps cumulative normalized weight (float64) to participant address (string) for weighted sampling.
// Cache holds the last NormalizedParticipantsCacheBlocks blocks.
func (k Keeper) GetNormalizedWeightedParticipants(blockHash []byte) (*btree.Map[float64, string], bool) {
	return k.normalizedWeightedParticipants.Get(blockHash)
}

// ProcessPendingRevalidationEvents gets all inference_validation events with needs_revalidation=true
// from the last finalized block and calls OnInferenceValidationNeedsRevalidation for each.
// When BlockRevalidationEventsProvider is set on the keeper, events are read from the provider
func (k Keeper) ProcessPendingRevalidationEvents(ctx context.Context, blockHeight int64, blockHash []byte) {
	var events []RevalidationEventInfo
	if k.blockRevalidationEventsProvider != nil {
		var err error
		events, err = k.blockRevalidationEventsProvider.GetInferenceValidationRevalidationEvents(ctx, blockHeight)
		if err != nil {
			// Log and skip; don't fail the block.
			k.Logger().Error("BlockRevalidationEventsProvider failed", "height", blockHeight, "error", err)
			return
		}
	}
	for _, e := range events {
		k.OnInferenceValidationNeedsRevalidation(ctx, e.InferenceId, e.Validator, blockHeight, blockHash)
	}
}
