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
	return k.UpdateDevshardHostEpochStats(ctx, epochIndex, participant, slotStats, false)
}

func (k Keeper) IncrementDevshardHostEscrowCount(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) error {
	return k.UpdateDevshardHostEpochStats(ctx, epochIndex, participant, types.DevshardSettlementHostStats{}, true)
}

func (k Keeper) UpdateDevshardHostEpochStats(
	ctx context.Context,
	epochIndex uint64,
	participant sdk.AccAddress,
	slotStats types.DevshardSettlementHostStats,
	incrementEscrowCount bool,
) error {
	var d devshardHostStatsDelta
	d.add(slotStats)
	return k.applyDevshardHostStatsDelta(ctx, epochIndex, participant, d, incrementEscrowCount)
}

// devshardHostStatsDelta sums one host's slots of a settlement, so its epoch stats
// are read and written once per host instead of once per slot.
type devshardHostStatsDelta struct {
	missed, invalid, required, completed uint64
	cost                                 uint64
	costOverflow                         bool
}

func (d *devshardHostStatsDelta) add(s types.DevshardSettlementHostStats) {
	d.missed += uint64(s.Missed)
	d.invalid += uint64(s.Invalid)
	d.required += uint64(s.RequiredValidations)
	d.completed += uint64(s.CompletedValidations)
	var carry uint64
	d.cost, carry = bits.Add64(d.cost, s.Cost, 0)
	if carry != 0 {
		d.costOverflow = true
	}
}

func (k Keeper) applyDevshardHostStatsDelta(
	ctx context.Context,
	epochIndex uint64,
	participant sdk.AccAddress,
	d devshardHostStatsDelta,
	incrementEscrowCount bool,
) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.DevshardHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.DevshardHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	if uint64(existing.Missed)+d.missed > math.MaxUint32 {
		return fmt.Errorf("missed overflow aggregating devshard host stats")
	}
	existing.Missed += uint32(d.missed)
	if uint64(existing.Invalid)+d.invalid > math.MaxUint32 {
		return fmt.Errorf("invalid overflow aggregating devshard host stats")
	}
	existing.Invalid += uint32(d.invalid)
	if d.costOverflow || existing.Cost > math.MaxUint64-d.cost {
		return fmt.Errorf("cost overflow aggregating devshard host stats")
	}
	existing.Cost += d.cost
	if uint64(existing.RequiredValidations)+d.required > math.MaxUint32 {
		return fmt.Errorf("required validations overflow aggregating devshard host stats")
	}
	existing.RequiredValidations += uint32(d.required)
	if uint64(existing.CompletedValidations)+d.completed > math.MaxUint32 {
		return fmt.Errorf("completed validations overflow aggregating devshard host stats")
	}
	existing.CompletedValidations += uint32(d.completed)
	if incrementEscrowCount {
		if existing.EscrowCount == math.MaxUint32 {
			return fmt.Errorf("escrow count overflow aggregating devshard host stats")
		}
		existing.EscrowCount++
	}
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
