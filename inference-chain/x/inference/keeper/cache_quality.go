package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetCacheQualityEpochSummary stores a CacheQualityEpochSummary for the given
// epoch and participant address.
func (k Keeper) SetCacheQualityEpochSummary(ctx context.Context, epochIndex uint64, participantAddress string, summary types.CacheQualityEpochSummary) error {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return err
	}
	return k.CacheQualityEpochSummaries.Set(ctx, collections.Join(epochIndex, addr), summary)
}

// GetCacheQualityEpochSummary retrieves the CacheQualityEpochSummary for a
// specific participant in a given epoch. Returns (summary, true) on hit.
func (k Keeper) GetCacheQualityEpochSummary(ctx context.Context, epochIndex uint64, participantAddress string) (types.CacheQualityEpochSummary, bool) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return types.CacheQualityEpochSummary{}, false
	}
	summary, err := k.CacheQualityEpochSummaries.Get(ctx, collections.Join(epochIndex, addr))
	if err != nil {
		return types.CacheQualityEpochSummary{}, false
	}
	return summary, true
}

// GetAllCacheQualityEpochSummariesForEpoch returns all CacheQualityEpochSummary
// records for the given epoch, keyed by participant bech32 address.
// This is called at epoch settlement to load summaries into WeightCalculator.
func (k Keeper) GetAllCacheQualityEpochSummariesForEpoch(ctx context.Context, epochIndex uint64) map[string]types.CacheQualityEpochSummary {
	result := make(map[string]types.CacheQualityEpochSummary)
	iter, err := k.CacheQualityEpochSummaries.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex))
	if err != nil {
		return result
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		addr := sdk.AccAddress(kv.Key.K2())
		result[addr.String()] = kv.Value
	}
	return result
}
