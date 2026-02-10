package keeper

import (
	"context"
	"math/rand"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/tidwall/btree"
)

// SampleNormalizedParticipantsForInference uses the normalized participants tree cached for the given
// committed block and model (keyed by (blockHash, modelId)) and a deterministic pseudo-random sequence derived from
// (blockHash, inferenceId) to pick up to NormalizedParticipantsSampleSize unique participants according to their weights.
// It returns up to NormalizedParticipantsSampleSize distinct participant addresses (may be fewer if the tree is empty,
// the cache is missing, or the tree has fewer unique participants than requested).
func (k Keeper) SampleNormalizedParticipantsForInference(blockHash []byte, modelId string, inferenceId string) []string {
	tree, ok := k.GetNormalizedWeightedParticipants(blockHash, modelId)
	if !ok || tree == nil {
		return nil
	}
	if inferenceId == "" {
		return nil
	}

	n := tree.Len()
	if n == 0 {
		return nil
	}
	// If participants count <= sample size, take all (deterministic order: ascending by cumulative weight).
	if n <= NormalizedParticipantsSampleSize {
		all := make([]string, 0, n)
		tree.Scan(func(weight float64, addr string) bool {
			all = append(all, addr)
			return true
		})
		return all
	}

	// Derive a deterministic seed from (blockHash || inferenceId) using the same random math as elsewhere.
	seed := calculations.SeedFromBytes(append(append([]byte(nil), blockHash...), []byte(inferenceId)...))
	rng := rand.New(rand.NewSource(seed))

	results := make([]string, 0, NormalizedParticipantsSampleSize)
	seen := make(map[string]struct{})
	iterations := 0

	for len(results) < NormalizedParticipantsSampleSize && iterations < NormalizedParticipantsMaxSampleIterations {
		iterations++
		r := rng.Float64() // in [0,1)
		var chosen string

		// Lower-bound seek: Ascend(r, ...) starts at first key >= r (O(log P) seek), then we take the first element only.
		tree.Ascend(r, func(weight float64, addr string) bool {
			chosen = addr
			return false // take only the first (smallest key >= r)
		})

		if chosen == "" {
			break
		}
		if _, already := seen[chosen]; !already {
			seen[chosen] = struct{}{}
			results = append(results, chosen)
		}
	}

	return results
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
	// Evict old entries from the selected-to-vote revalidation cache (same window) and remove
	// corresponding ActiveInvalidations (invalidator, inferenceId) so they don't outlive the cache.
	evicted := k.revalidationVoteParticipants.ClearByHeight(currentBlockHeight - NormalizedParticipantsCacheBlocks)
	for _, e := range evicted {
		if e.InferenceId == "" || e.Invalidator == "" {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(e.Invalidator)
		if err != nil {
			continue
		}
		_ = k.ActiveInvalidations.Remove(ctx, collections.Join(addr, e.InferenceId))
	}
	// Evict old entries from the ephemeral revalidation votes cache (same window).
	k.ephemeralRevalidationVotes.ClearByHeight(currentBlockHeight - NormalizedParticipantsCacheBlocks)
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
// and adds them to the cache keyed by (blockHash, modelId). Call once per commit from the Precommiter hook.
func (k *Keeper) SetNormalizedParticipantsForCommittedBlock(ctx context.Context, blockHeight int64, blockHash []byte) {
	effEpoch, ok := k.GetEffectiveEpochIndex(ctx)
	if !ok {
		return
	}
	// Parent epoch group (no modelId) — keep this entry for any global uses.
	parentEpoch, found := k.GetEpochGroupData(ctx, effEpoch, "")
	if !found {
		return
	}
	parentWeights := validationWeightsToParticipantWeights(parentEpoch.GetValidationWeights())
	k.normalizedWeightedParticipants.Add(blockHash, blockHeight, "", parentWeights)

	// Per-model epoch groups: build a separate normalized tree per model so revalidation/voting
	// can select participants that actually support the inference's model.
	for _, modelId := range parentEpoch.SubGroupModels {
		if modelId == "" {
			continue
		}
		subEpoch, found := k.GetEpochGroupData(ctx, effEpoch, modelId)
		if !found {
			continue
		}
		modelWeights := validationWeightsToParticipantWeights(subEpoch.GetValidationWeights())
		if len(modelWeights) == 0 {
			continue
		}
		k.normalizedWeightedParticipants.Add(blockHash, blockHeight, modelId, modelWeights)
	}
}

// GetNormalizedWeightedParticipants returns the BTree for the given (block hash, modelId) if present in the cache.
// The tree maps cumulative normalized weight (float64) to participant address (string) for weighted sampling.
// Cache holds the last NormalizedParticipantsCacheBlocks blocks.
func (k Keeper) GetNormalizedWeightedParticipants(blockHash []byte, modelId string) (*btree.Map[float64, string], bool) {
	return k.normalizedWeightedParticipants.Get(blockHash, modelId)
}

// IsParticipantEligibleToVoteOnRevalidation returns true if participantAddress is in the deterministic list
// of participants selected to vote on the revalidation for inferenceId that was emitted in the block at blockHeight.
// The list is computed and cached when revalidation events are processed (Precommiter); cache is evicted
// after NormalizedParticipantsCacheBlocks blocks. blockHeight is the height of the block where the revalidation event was emitted.
func (k Keeper) IsParticipantEligibleToVoteOnRevalidation(blockHeight int64, inferenceId string, participantAddress string) bool {
	return k.revalidationVoteParticipants.Contains(blockHeight, inferenceId, participantAddress)
}

// GetRevalidationVoteWeight returns the capped vote weight for (inferenceId, participantAddress)
// from the in-memory revalidationVoteParticipants cache, if present.
func (k Keeper) GetRevalidationVoteWeight(blockHeight int64, inferenceId string, participantAddress string) (int64, bool) {
	return k.revalidationVoteParticipants.GetWeight(blockHeight, inferenceId, participantAddress)
}

// ProcessPendingRevalidationEvents gets all inference_validation events with needs_revalidation=true
// from the last finalized block, caches the list of participants selected to vote per (blockHeight, inferenceId),
// Participants are sampled from the normalized tree for the specific model used by the inference.
func (k *Keeper) ProcessPendingRevalidationEvents(ctx context.Context, blockHeight int64, blockHash []byte) {
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
	// Populate selected-to-vote cache and start revalidation vote (keeper or ephemeral cache) per inference.
	effEpoch, _ := k.GetEffectiveEpochIndex(ctx)
	for _, e := range events {
		inference, found := k.GetInference(ctx, e.InferenceId)
		if !found {
			k.Logger().Error("ProcessPendingRevalidationEvents: inference not found for revalidation event",
				"height", blockHeight, "inference_id", e.InferenceId)
			continue
		}
		modelId := inference.Model
		participants := k.SampleNormalizedParticipantsForInference(blockHash, modelId, e.InferenceId)

		groupData, foundGroup := k.GetEpochGroupData(ctx, effEpoch, modelId)
		if !foundGroup {
			continue
		}
		weightMap := make(map[string]int64)
		for _, w := range validationWeightsToParticipantWeights(groupData.GetValidationWeights()) {
			weightMap[w.Address] = w.Weight
		}
		// Build selected participant -> raw weight map (including invalidator) for this inference.
		selected := make(map[string]int64)
		var totalEligibleWeight int64

		if w := weightMap[e.Validator]; w > 0 {
			selected[e.Validator] = w
			totalEligibleWeight += w
		}
		for _, p := range participants {
			if p == e.Validator {
				continue
			}
			if w := weightMap[p]; w > 0 {
				selected[p] = w
				totalEligibleWeight += w
			}
		}
		if totalEligibleWeight == 0 {
			continue
		}

		// Apply hard 20% cap with redistribution effect: each participant's vote weight
		// is capped at 20% of totalEligibleWeight; the new total is the sum of capped weights.
		const capPercent int64 = 20
		capLimit := (totalEligibleWeight * capPercent) / 100
		if capLimit <= 0 {
			continue
		}

		capped := make(map[string]int64, len(selected))
		var cappedTotal int64
		for addr, w := range selected {
			if w > capLimit {
				w = capLimit
			}
			capped[addr] = w
			cappedTotal += w
		}
		if cappedTotal == 0 {
			continue
		}

		// Store invalidator and capped weights in the revalidation vote participants cache so that
		// revalidation voting can use the capped weights per (inferenceId, participant), and
		// ActiveInvalidations can be cleaned up when the cache is evicted (same invalidator key).
		k.revalidationVoteParticipants.Add(blockHeight, e.InferenceId, e.Validator, capped)

		invalidatorWeight := capped[e.Validator]
		// Initiate vote for the invalidator (first vote) with capped invalidator weight.
		if err := k.StartRevalidationVote(ctx, e.InferenceId, e.Validator, invalidatorWeight, cappedTotal, blockHeight); err != nil {
			k.Logger().Error("ProcessPendingRevalidationEvents: StartRevalidationVote failed", "inference_id", e.InferenceId, "error", err)
		}
	}
}
