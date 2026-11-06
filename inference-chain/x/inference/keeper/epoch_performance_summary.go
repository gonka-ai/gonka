package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochPerformanceSummary set a specific epochPerformanceSummary in the store from its index
func (k Keeper) SetEpochPerformanceSummary(ctx context.Context, epochPerformanceSummary types.EpochPerformanceSummary) error {
	addr, err := sdk.AccAddressFromBech32(epochPerformanceSummary.ParticipantId)
	if err != nil {
		return err
	}

	return k.EpochPerformanceSummaries.Set(ctx, collections.Join(addr, epochPerformanceSummary.EpochIndex), storedPerformanceSummary(epochPerformanceSummary, addr))
}

// storedPerformanceSummary drops the participant and epoch the key holds; restoredPerformanceSummary
// fills them back. The full record is kept when the key would not restore the same address string.
func storedPerformanceSummary(s types.EpochPerformanceSummary, participant sdk.AccAddress) types.EpochPerformanceSummary {
	trimmed := s
	trimmed.ParticipantId, trimmed.EpochIndex = "", 0
	if participant.String() != s.ParticipantId || trimmed == (types.EpochPerformanceSummary{}) {
		return s
	}
	return trimmed
}

func restoredPerformanceSummary(key collections.Pair[sdk.AccAddress, uint64], s types.EpochPerformanceSummary) types.EpochPerformanceSummary {
	if s.ParticipantId == "" {
		s.ParticipantId, s.EpochIndex = key.K1().String(), key.K2()
	}
	return s
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
	key := collections.Join(addr, epochIndex)
	v, err := k.EpochPerformanceSummaries.Get(ctx, key)
	if err != nil {
		return val, false
	}
	return restoredPerformanceSummary(key, v), true
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
	kvs, err := it.KeyValues()
	if err != nil {
		return nil
	}
	list = make([]types.EpochPerformanceSummary, len(kvs))
	for i, kv := range kvs {
		list[i] = restoredPerformanceSummary(kv.Key, kv.Value)
	}
	return list
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
		kv, err := it.KeyValue()
		if err != nil {
			return nil
		}
		out = append(out, restoredPerformanceSummary(kv.Key, kv.Value))
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
		key := collections.Join(addr, epochIndex)
		v, err := k.EpochPerformanceSummaries.Get(ctx, key)
		if err != nil {
			continue
		}
		summaries = append(summaries, restoredPerformanceSummary(key, v))
	}
	return summaries
}
