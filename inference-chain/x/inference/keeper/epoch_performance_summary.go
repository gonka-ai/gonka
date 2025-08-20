package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochPerformanceSummary set a specific epochPerformanceSummary in the store from its index
func (k Keeper) SetEpochPerformanceSummary(ctx context.Context, epochPerformanceSummary types.EpochPerformanceSummary) {
	addr := sdk.MustAccAddressFromBech32(epochPerformanceSummary.ParticipantId)
	if err := k.EpochPerformanceSummaries.Set(ctx, collections.Join(addr, epochPerformanceSummary.EpochIndex), epochPerformanceSummary); err != nil {
		panic(err)
	}
}

// GetEpochPerformanceSummary returns a epochPerformanceSummary from its index
func (k Keeper) GetEpochPerformanceSummary(
	ctx context.Context,
	epochIndex uint64,
	participantId string,
) (val types.EpochPerformanceSummary, found bool) {
	addr, err := sdk.AccAddressFromBech32(participantId)
	if err != nil {
		return val, false
	}
	v, err := k.EpochPerformanceSummaries.Get(ctx, collections.Join(addr, epochIndex))
	if err != nil {
		return val, false
	}
	return v, true
}

// RemoveEpochPerformanceSummary removes a epochPerformanceSummary from the store
func (k Keeper) RemoveEpochPerformanceSummary(
	ctx context.Context,
	epochIndex uint64,
	participantId string,
) {
	addr, err := sdk.AccAddressFromBech32(participantId)
	if err != nil {
		return
	}
	_ = k.EpochPerformanceSummaries.Remove(ctx, collections.Join(addr, epochIndex))
}

// GetAllEpochPerformanceSummary returns all epochPerformanceSummary
func (k Keeper) GetAllEpochPerformanceSummary(ctx context.Context) (list []types.EpochPerformanceSummary) {
	it, err := k.EpochPerformanceSummaries.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer it.Close()
	values, err := it.Values()
	if err != nil {
		return nil
	}
	return values
}

// GetEpochPerformanceSummariesByParticipant returns all epochPerformanceSummary for a specific participant
func (k Keeper) GetEpochPerformanceSummariesByParticipant(ctx context.Context, participantId string) (list []types.EpochPerformanceSummary) {
	addr, err := sdk.AccAddressFromBech32(participantId)
	if err != nil {
		return nil
	}
	it, err := k.EpochPerformanceSummaries.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, uint64](addr))
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []types.EpochPerformanceSummary
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func (k Keeper) GetParticipantsEpochSummaries(
	ctx context.Context,
	participantIds []string,
	epochIndex uint64,
) []types.EpochPerformanceSummary {
	var summaries []types.EpochPerformanceSummary
	for _, participantId := range participantIds {
		addr, err := sdk.AccAddressFromBech32(participantId)
		if err != nil {
			continue
		}
		v, err := k.EpochPerformanceSummaries.Get(ctx, collections.Join(addr, epochIndex))
		if err != nil {
			continue
		}
		summaries = append(summaries, v)
	}
	return summaries
}
