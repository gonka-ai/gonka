package keeper

import (
	"context"
	"fmt"
	"math"
	"math/bits"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetDevshardHostEpochStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) (types.DevshardHostEpochStats, bool) {
	v, err := k.DevshardHostEpochStatsMap.Get(ctx, collections.Join(epochIndex, participant))
	if err != nil {
		return types.DevshardHostEpochStats{}, false
	}
	return v, true
}

func (k Keeper) AggregateDevshardHostStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress, slotStats types.DevshardSettlementHostStats) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.DevshardHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.DevshardHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.Missed += slotStats.Missed
	existing.Invalid += slotStats.Invalid
	if existing.Cost > math.MaxUint64-slotStats.Cost {
		return fmt.Errorf("cost overflow aggregating devshard host stats")
	}
	existing.Cost += slotStats.Cost
	existing.RequiredValidations += slotStats.RequiredValidations
	existing.CompletedValidations += slotStats.CompletedValidations
	return k.DevshardHostEpochStatsMap.Set(ctx, key, existing)
}

func (k Keeper) IncrementDevshardHostEscrowCount(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.DevshardHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.DevshardHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.EscrowCount++
	return k.DevshardHostEpochStatsMap.Set(ctx, key, existing)
}

// AggregateDevshardHostStatsIntoCurrentEpochStats merges one slot's devshard
// settlement stats into the participant's current-epoch counters.
func AggregateDevshardHostStatsIntoCurrentEpochStats(
	participant *types.Participant,
	slotStats types.DevshardSettlementHostStats,
	assignedPerSlot uint64,
) error {

	if participant == nil {
		return fmt.Errorf("participant is nil")
	}

	ensureParticipantEpochStats(participant)

	// uint64 safe because slotStats.Missed/Invalid are uint32
	missed := uint64(slotStats.Missed)
	invalid := uint64(slotStats.Invalid)

	if missed > assignedPerSlot {
		return fmt.Errorf("missed requests (%d) exceeds assigned per slot (%d) for %s", missed, assignedPerSlot, participant.Address)
	}
	completed := assignedPerSlot - missed

	if invalid > completed {
		return fmt.Errorf("invalid inferences (%d) exceeds completed requests (%d) for %s", invalid, completed, participant.Address)
	}
	validated := completed - invalid

	nextMissed, carry := bits.Add64(participant.CurrentEpochStats.MissedRequests, missed, 0)
	if carry != 0 {
		return fmt.Errorf("participant missed requests overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.MissedRequests = nextMissed

	nextInvalidated, carry := bits.Add64(participant.CurrentEpochStats.InvalidatedInferences, invalid, 0)
	if carry != 0 {
		return fmt.Errorf("participant invalidated inferences overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.InvalidatedInferences = nextInvalidated

	nextInferenceCount, carry := bits.Add64(participant.CurrentEpochStats.InferenceCount, completed, 0)
	if carry != 0 {
		return fmt.Errorf("participant inference count overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.InferenceCount = nextInferenceCount

	nextValidated, carry := bits.Add64(participant.CurrentEpochStats.ValidatedInferences, validated, 0)
	if carry != 0 {
		return fmt.Errorf("participant validated inferences overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.ValidatedInferences = nextValidated

	return nil
}
