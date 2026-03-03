package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// GetAllContinuousPoCEpochSummariesForEpoch returns all ContinuousPoCEpochSummary records
// for a given epoch, keyed by participant bech32 address.
// Called during epoch settlement to load continuous PoC weights.
func (k Keeper) GetAllContinuousPoCEpochSummariesForEpoch(ctx context.Context, epochIndex uint64) map[string]types.ContinuousPoCEpochSummary {
	result := make(map[string]types.ContinuousPoCEpochSummary)

	iter, err := k.ContinuousPoCEpochSummaries.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex))
	if err != nil {
		k.LogError("[ContinuousPoC] Failed to iterate epoch summaries", types.PoC,
			"epoch", epochIndex, "error", err)
		return result
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		summary, err := iter.Value()
		if err != nil {
			continue
		}
		result[summary.ParticipantAddress] = summary
	}
	return result
}

// GetContinuousPoCEpochSummary returns the continuous PoC epoch summary for a specific participant.
func (k Keeper) GetContinuousPoCEpochSummary(ctx context.Context, epochIndex uint64, participantAddress string) (types.ContinuousPoCEpochSummary, bool) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return types.ContinuousPoCEpochSummary{}, false
	}
	key := collections.Join(epochIndex, addr)
	summary, err := k.ContinuousPoCEpochSummaries.Get(ctx, key)
	if err != nil {
		return types.ContinuousPoCEpochSummary{}, false
	}
	return summary, true
}
