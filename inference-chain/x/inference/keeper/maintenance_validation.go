package keeper

import (
	"context"
	"fmt"

	cosmossdk_math "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/x/inference/types"
)

// maxEpochPhaseOverlapChecks bounds the per-call iteration of the PoC/DKG
// overlap loop so a pathological MaintenanceMaxWindowBlocks setting cannot
// turn a single Schedule message into an unbounded compute load. The window
// has to cross this many epochs to trigger; well above any realistic
// maintenance horizon, so legitimate schedules are unaffected.
const maxEpochPhaseOverlapChecks int64 = 10_000

// checkEpochPhaseOverlap verifies the proposed maintenance window does not overlap
// epoch-critical PoC commit/exchange or DKG (SetNewValidators) phases.
//
// Iteration shape: walk forward in epochs starting from the epoch that
// contains startHeight, stopping the first time the next epoch's PoC start is
// past endHeight. The previous fixed lookahead of 5 epochs was unsafe — the
// upper bound on MaintenanceMaxWindowBlocks (1e15) is many orders of
// magnitude above 5 epoch lengths, and startHeight itself can sit many epochs
// past the effective epoch (governance-bound only by lead time), so any
// fixed lookahead can leave the window's tail in an unscanned epoch's busy
// region.
func (k Keeper) checkEpochPhaseOverlap(ctx context.Context, startHeight int64, durationBlocks uint64, mp *types.MaintenanceParams) error {
	endHeight := startHeight + int64(durationBlocks) - 1

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}
	ep := params.EpochParams
	if ep == nil {
		return nil // no epoch params, skip check
	}
	if ep.EpochLength <= 0 {
		// Invalid epoch params are rejected at SetParams; defending here keeps
		// this function from entering an infinite NextEpochContext walk if a
		// malformed state ever lands.
		return nil
	}

	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		return nil // no epoch yet, skip check
	}

	ec := types.NewEpochContext(*effectiveEpoch, *ep)

	// Fast-forward ec to the epoch that contains startHeight. Without this,
	// a window scheduled many epochs in the future would force the loop to
	// walk every intermediate epoch — wasted work and a DoS surface.
	// Uniform EpochLength is assumed (NextEpochContext advances by exactly
	// EpochLength); historical re-anchoring is not needed because the schedule
	// path operates entirely against the current epoch params.
	if ec.EpochIndex > 0 && startHeight > ec.StartOfPoC() {
		steps := (startHeight - ec.StartOfPoC()) / ep.EpochLength
		ec.EpochIndex += uint64(steps)
		ec.PocStartBlockHeight += steps * ep.EpochLength
	}

	for checked := int64(0); ; checked++ {
		if checked >= maxEpochPhaseOverlapChecks {
			return fmt.Errorf("maintenance window spans more than %d epochs: %w",
				maxEpochPhaseOverlapChecks, types.ErrMaintenanceDurationExceeded)
		}

		if ec.EpochIndex != 0 {
			// PoC commit/exchange overlap
			pocStart := ec.StartOfPoC()
			pocExchangeEnd := ec.PoCExchangeDeadline()
			if startHeight <= pocExchangeEnd && endHeight >= pocStart {
				return types.ErrMaintenanceOverlapsPoCPhase
			}

			// PoC validation overlap
			valStart := ec.StartOfPoCValidation()
			valEnd := ec.EndOfPoCValidation()
			if startHeight <= valEnd && endHeight >= valStart {
				return types.ErrMaintenanceOverlapsPoCPhase
			}

			// SetNewValidators (DKG) overlap
			setNewVal := ec.SetNewValidators()
			if startHeight <= setNewVal && endHeight >= setNewVal {
				return types.ErrMaintenanceOverlapsDKGPhase
			}
		}

		next := ec.NextEpochContext()
		if next.StartOfPoC() > endHeight {
			return nil
		}
		ec = next
	}
}

// checkConcurrencyLimits verifies that adding a new reservation does not exceed
// concurrent participant count or power caps at scheduling time.
//
// Instead of scanning a start-height index (which suffered from unbounded growth
// of completed entries), this iterates the bounded set of ACTIVE + SCHEDULED
// reservations derived from MaintenanceStates. The size is at most
// 2 * total_participants, and in practice much smaller.
//
// All power math is done in math.Int (cosmossdk_math.Int) to avoid int64
// overflow on either token aggregation or the bps multiplication.
func (k Keeper) checkConcurrencyLimits(ctx context.Context, startHeight int64, durationBlocks uint64, participant sdk.AccAddress, mp *types.MaintenanceParams) error {
	endHeight := startHeight + int64(durationBlocks) - 1

	reservations, err := k.collectActiveAndScheduledReservations(ctx)
	if err != nil {
		return fmt.Errorf("failed to check concurrency: %w", err)
	}

	concurrentCount := uint32(0)
	concurrentPower := cosmossdk_math.ZeroInt()
	participantPower := k.getParticipantPower(ctx, participant)

	for _, r := range reservations {
		rEnd := r.StartHeight + int64(r.DurationBlocks) - 1
		if r.StartHeight <= endHeight && rEnd >= startHeight {
			concurrentCount++
			rAddr, addrErr := sdk.AccAddressFromBech32(r.Participant)
			if addrErr == nil {
				concurrentPower = concurrentPower.Add(k.getParticipantPower(ctx, rAddr))
			}
		}
	}

	// Check count cap (including the new reservation)
	if concurrentCount+1 > mp.MaintenanceMaxConcurrentValidators {
		return types.ErrMaintenanceConcurrentCountExceeded
	}

	// Check power cap (including this participant's power) using integer math.
	totalPower := k.getTotalConsensusPower(ctx)
	if totalPower.IsPositive() && mp.MaintenanceMaxConcurrentPowerBps > 0 {
		maxPower := totalPower.MulRaw(int64(mp.MaintenanceMaxConcurrentPowerBps)).QuoRaw(10000)
		if concurrentPower.Add(participantPower).GT(maxPower) {
			return types.ErrMaintenanceConcurrentPowerExceeded
		}
	}

	return nil
}

// validatorPower returns the consensus power of v as a math.Int. Using
// TokensFromConsensusPower's inverse here would lose precision; instead we
// just return v.Tokens, which is the effective bonded-token weight that
// underlies consensus power. The caller compares this against bps of the
// total bonded tokens, so the units are consistent.
func validatorPower(v stakingtypes.Validator) cosmossdk_math.Int {
	return v.Tokens
}

// checkParticipantOverlap checks that the proposed window does not overlap
// with any existing scheduled/active reservation for the same participant.
// Since MaintenanceState tracks at most 1 active + 1 scheduled reservation per
// participant, this is a simple direct lookup — no index scan needed.
func (k Keeper) checkParticipantOverlap(ctx context.Context, participant sdk.AccAddress, startHeight int64, durationBlocks uint64) error {
	endHeight := startHeight + int64(durationBlocks) - 1

	state, found := k.GetMaintenanceState(ctx, participant)
	if !found {
		return nil
	}

	// Check active reservation overlap
	if state.ActiveReservationId != 0 {
		if r, ok := k.GetMaintenanceReservation(ctx, state.ActiveReservationId); ok {
			rEnd := r.StartHeight + int64(r.DurationBlocks) - 1
			if r.StartHeight <= endHeight && rEnd >= startHeight {
				return types.ErrMaintenanceOverlap
			}
		}
	}

	// Check scheduled reservation overlap
	if state.ScheduledReservationId != 0 {
		if r, ok := k.GetMaintenanceReservation(ctx, state.ScheduledReservationId); ok {
			rEnd := r.StartHeight + int64(r.DurationBlocks) - 1
			if r.StartHeight <= endHeight && rEnd >= startHeight {
				return types.ErrMaintenanceOverlap
			}
		}
	}

	return nil
}

// getParticipantPower returns the consensus power (in bonded-token units, as
// math.Int) for a participant. Returns ZeroInt if the participant is not a
// validator. This is an O(1) lookup via the staking keeper — no full
// validator-set scan.
func (k Keeper) getParticipantPower(ctx context.Context, participant sdk.AccAddress) cosmossdk_math.Int {
	// In Gonka, the participant account address bytes match the validator
	// operator address bytes, so we can convert directly.
	v, err := k.Staking.GetValidator(ctx, sdk.ValAddress(participant))
	if err != nil {
		return cosmossdk_math.ZeroInt()
	}
	return validatorPower(v)
}

// getTotalConsensusPower returns the total bonded-token power across all
// validators as a math.Int. Uses GetLastTotalPower (which returns the staking
// module's cached LastTotalPower in *consensus power* units, then re-scales
// back to bonded-token units to keep units consistent with getParticipantPower).
//
// We multiply by DefaultPowerReduction to convert "consensus power units" back
// to "bonded-token units" so that the bps comparison in checkConcurrencyLimits
// is unit-consistent: both sides are bonded-token amounts.
func (k Keeper) getTotalConsensusPower(ctx context.Context) cosmossdk_math.Int {
	totalConsPower, err := k.Staking.GetLastTotalPower(ctx)
	if err != nil {
		return cosmossdk_math.ZeroInt()
	}
	// LastTotalPower is stored in consensus-power units; reverse the
	// PowerReduction to get bonded-token units.
	return totalConsPower.Mul(sdk.DefaultPowerReduction)
}
