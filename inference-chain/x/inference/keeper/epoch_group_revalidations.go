package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupRevalidation stores one revalidation entry by (epoch, participant, inferenceID).
func (k Keeper) SetEpochGroupRevalidation(ctx context.Context, epochIndex uint64, participant string, inferenceID string) error {
	return k.EpochGroupRevalidationEntry.Set(ctx, collections.Join3(epochIndex, participant, inferenceID))
}

// GetEpochGroupRevalidations returns epoch-group revalidations for (participant, epoch).
func (k Keeper) GetEpochGroupRevalidations(
	ctx context.Context,
	participant string,
	epochIndex uint64,
) (val types.EpochGroupValidations, found bool) {
	revalidatedInferences := make([]string, 0)
	iter, err := k.EpochGroupRevalidationEntry.Iterate(ctx, collections.NewSuperPrefixedTripleRange[uint64, string, string](epochIndex, participant))
	if err != nil {
		return val, false
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, keyErr := iter.Key()
		if keyErr != nil {
			return val, false
		}
		revalidatedInferences = append(revalidatedInferences, key.K3())
	}
	if len(revalidatedInferences) == 0 {
		return val, false
	}
	return types.EpochGroupValidations{
		Participant:         participant,
		EpochIndex:          epochIndex,
		ValidatedInferences: revalidatedInferences,
	}, true
}
