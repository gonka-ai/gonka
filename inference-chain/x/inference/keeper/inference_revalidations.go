package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// AddRevalidationVote records a single revalidation vote for (inferenceId, participant).
// Key is (inferenceId, participant); value is RevalidationVoteRecord.
func (k *Keeper) AddRevalidationVoteToStore(ctx context.Context, inferenceId string, participant string, weight int64, pass bool) error {
	key := collections.Join(inferenceId, participant)
	return k.InferenceRevalidations.Set(ctx, key, types.RevalidationVoteRecord{Weight: weight, Pass: pass})
}

// GetInferenceRevalidations returns all revalidation votes for the given inference (aggregated from stored keys).
func (k *Keeper) GetInferenceRevalidationsFromStore(ctx context.Context, inferenceId string) (*types.InferenceRevalidations, bool) {
	rng := collections.NewPrefixedPairRange[string, string](inferenceId)
	iter, err := k.InferenceRevalidations.Iterate(ctx, rng)
	if err != nil {
		return nil, false
	}
	defer iter.Close()
	var votes []*types.RevalidationVote
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return nil, false
		}
		participant := kv.Key.K2()
		rec := kv.Value
		votes = append(votes, &types.RevalidationVote{Address: participant, Weight: rec.Weight, Pass: rec.Pass})
	}
	if len(votes) == 0 {
		return nil, false
	}
	return &types.InferenceRevalidations{Votes: votes}, true
}

// ClearInferenceRevalidations removes all revalidation votes for the inference and its total eligible weight.
func (k *Keeper) ClearInferenceRevalidationsFromStore(ctx context.Context, inferenceId string) error {
	rng := collections.NewPrefixedPairRange[string, string](inferenceId)
	iter, err := k.InferenceRevalidations.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	keys, err := iter.Keys()
	iter.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := k.InferenceRevalidations.Remove(ctx, key); err != nil {
			return err
		}
	}
	_ = k.InferenceRevalidationTotalEligibleWeight.Remove(ctx, inferenceId)
	return nil
}
