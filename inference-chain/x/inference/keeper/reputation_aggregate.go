package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// ReputationMissTotals returns the participant's summary count and miss-rate sum above cutoff,
// reading only summaries newer than the stored aggregate (they are written once, in epoch order).
func (k Keeper) ReputationMissTotals(ctx context.Context, participantId string, cutoff decimal.Decimal) (int64, decimal.Decimal, error) {
	addr, err := sdk.AccAddressFromBech32(participantId)
	if err != nil {
		return 0, decimal.Zero, nil
	}
	agg, err := k.ReputationAggregates.Get(ctx, addr)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, decimal.Zero, err
	}
	sum := decimal.Zero
	if agg.EpochCount > 0 {
		storedCutoff, cutoffErr := decimal.NewFromString(agg.MissCutoff)
		storedSum, sumErr := decimal.NewFromString(agg.MissSum)
		if cutoffErr == nil && sumErr == nil && storedCutoff.Equal(cutoff) {
			sum = storedSum
		} else {
			agg = types.ReputationAggregate{}
		}
	}

	rng := collections.NewPrefixedPairRange[sdk.AccAddress, uint64](addr)
	if agg.EpochCount > 0 {
		rng = rng.StartExclusive(agg.LastEpochIndex)
	}
	it, err := k.EpochPerformanceSummaries.Iterate(ctx, rng)
	if err != nil {
		return 0, decimal.Zero, err
	}
	defer it.Close()
	changed := false
	for ; it.Valid(); it.Next() {
		kv, err := it.KeyValue()
		if err != nil {
			return 0, decimal.Zero, err
		}
		missRate := calculations.EpochMissRate(kv.Value.InferenceCount, kv.Value.MissedRequests)
		if missRate.GreaterThan(cutoff) {
			sum = sum.Add(missRate)
		}
		agg.EpochCount++
		agg.LastEpochIndex = kv.Key.K2()
		changed = true
	}
	if changed {
		agg.MissSum = sum.String()
		agg.MissCutoff = cutoff.String()
		if err := k.ReputationAggregates.Set(ctx, addr, agg); err != nil {
			return 0, decimal.Zero, err
		}
	}
	return int64(agg.EpochCount), sum, nil
}
