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
	key := collections.Join(epochIndex, participant)
	existing, err := k.DevshardHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.DevshardHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	if existing.Missed > math.MaxUint32-slotStats.Missed {
		return fmt.Errorf("missed overflow aggregating devshard host stats")
	}
	existing.Missed += slotStats.Missed
	if existing.Invalid > math.MaxUint32-slotStats.Invalid {
		return fmt.Errorf("invalid overflow aggregating devshard host stats")
	}
	existing.Invalid += slotStats.Invalid
	if existing.Cost > math.MaxUint64-slotStats.Cost {
		return fmt.Errorf("cost overflow aggregating devshard host stats")
	}
	existing.Cost += slotStats.Cost
	if existing.RequiredValidations > math.MaxUint32-slotStats.RequiredValidations {
		return fmt.Errorf("required validations overflow aggregating devshard host stats")
	}
	existing.RequiredValidations += slotStats.RequiredValidations
	if existing.CompletedValidations > math.MaxUint32-slotStats.CompletedValidations {
		return fmt.Errorf("completed validations overflow aggregating devshard host stats")
	}
	existing.CompletedValidations += slotStats.CompletedValidations
	if existing.Validated > math.MaxUint32-slotStats.Validated {
		return fmt.Errorf("validated overflow aggregating devshard host stats")
	}
	existing.Validated += slotStats.Validated
	if existing.Finished > math.MaxUint32-slotStats.Finished {
		return fmt.Errorf("finished overflow aggregating devshard host stats")
	}
	existing.Finished += slotStats.Finished
	if incrementEscrowCount {
		if existing.EscrowCount == math.MaxUint32 {
			return fmt.Errorf("escrow count overflow aggregating devshard host stats")
		}
		existing.EscrowCount++
	}
	return k.DevshardHostEpochStatsMap.Set(ctx, key, existing)
}

const DevshardSprtPassSlack = 2

// DevshardPassPolicy says how a settlement's host stats become SPRT passes.
type DevshardPassPolicy struct {
	LegacyValidated   bool
	ValidationRateBps uint32
}

// DevshardPassPolicyFor resolves the policy from the approved-versions list.
func DevshardPassPolicyFor(approved []*types.DevshardApprovedVersion, version string, validationRateBps uint32) DevshardPassPolicy {
	policy := DevshardPassPolicy{ValidationRateBps: validationRateBps}
	if len(approved) == 0 {
		return policy
	}
	for _, v := range approved {
		if v != nil && v.Name == version {
			policy.LegacyValidated = !v.ReportsValidated
			return policy
		}
	}
	policy.LegacyValidated = true
	return policy
}

// DevshardSprtPassCap bounds credited passes by the escrow's sampling rate.
func DevshardSprtPassCap(finished uint64, validationRateBps uint32) uint64 {
	return 2*finished*uint64(validationRateBps)/10000 + DevshardSprtPassSlack
}

func (p DevshardPassPolicy) checkSlot(hs *types.DevshardSettlementHostStats, completed, slotCount uint64) error {
	finished := uint64(hs.Finished)
	if p.LegacyValidated {
		if uint64(hs.Validated) > completed*slotCount {
			return fmt.Errorf("validated count %d exceeds completed %d times %d slots", hs.Validated, completed, slotCount)
		}
		return nil
	}
	if finished > completed {
		return fmt.Errorf("finished count %d exceeds completed %d", hs.Finished, completed)
	}
	if uint64(hs.Invalid) > finished {
		return fmt.Errorf("invalid count %d exceeds finished %d", hs.Invalid, hs.Finished)
	}
	if uint64(hs.Validated) > finished*slotCount {
		return fmt.Errorf("validated count %d exceeds finished %d times %d slots", hs.Validated, hs.Finished, slotCount)
	}
	return nil
}

// AggregateDevshardHostStatsIntoCurrentEpochStats merges one slot's devshard
// settlement stats into the participant's current-epoch counters.
func AggregateDevshardHostStatsIntoCurrentEpochStats(
	participant *types.Participant,
	slotStats types.DevshardSettlementHostStats,
	assignedPerSlot uint64,
	slotCount uint64,
	policy DevshardPassPolicy,
) error {

	if participant == nil {
		return fmt.Errorf("participant is nil")
	}

	ensureParticipantEpochStats(participant)

	// uint64 safe because slotStats.Missed/Invalid/Validated are uint32
	missed := uint64(slotStats.Missed)
	invalid := uint64(slotStats.Invalid)
	validated := uint64(slotStats.Validated)

	if missed > assignedPerSlot {
		return fmt.Errorf("missed requests (%d) exceeds assigned per slot (%d) for %s", missed, assignedPerSlot, participant.Address)
	}
	completed := assignedPerSlot - missed

	if invalid > completed {
		return fmt.Errorf("invalid inferences (%d) exceeds completed requests (%d) for %s", invalid, completed, participant.Address)
	}
	if err := policy.checkSlot(&slotStats, completed, slotCount); err != nil {
		return fmt.Errorf("%w for %s", err, participant.Address)
	}
	if policy.LegacyValidated {
		validated = completed - invalid
	} else {
		validated = min(validated, DevshardSprtPassCap(uint64(slotStats.Finished), policy.ValidationRateBps))
	}

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
