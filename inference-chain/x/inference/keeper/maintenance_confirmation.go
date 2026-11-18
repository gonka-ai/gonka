package keeper

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SnapshotMaintenanceForConfirmation records the existing reservations that
// overlap the generation interval at trigger time. It scans only active and
// scheduled windows, not accumulated reservation history. Window lengths come
// from the reservation so changes to the maximum do not hide old windows.
//
// The trigger is computed in EndBlock, after transactions. Its randomness is
// already available during that block, so same-block reservations must be
// excluded too. Unknown legacy creation heights do not establish eligibility;
// maintenance must remain disabled until this upgrade on chains without them.
func (k Keeper) SnapshotMaintenanceForConfirmation(ctx context.Context, event *types.ConfirmationPoCEvent) ([]uint64, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if params.EpochParams == nil {
		return nil, fmt.Errorf("epoch params not found")
	}
	generationEnd := event.GetGenerationEnd(params.EpochParams)
	reservations, err := k.confirmationMaintenanceReservations(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for _, r := range reservations {
		if r.CreatedAtBlockHeight <= 0 || r.CreatedAtBlockHeight >= event.TriggerHeight || r.StartHeight > generationEnd {
			continue
		}
		endHeight, err := confirmationMaintenanceEnd(r)
		if err != nil {
			return nil, err
		}
		if endHeight >= event.GenerationStartHeight {
			ids = append(ids, r.ReservationId)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// This snapshot is authoritative for later penalties. Unlike advisory/public
// index queries, it must not silently truncate or skip dangling entries: abort
// creation of the event rather than persist an incomplete exemption list.
func (k Keeper) confirmationMaintenanceReservations(ctx context.Context) ([]types.MaintenanceReservation, error) {
	var reservations []types.MaintenanceReservation
	for _, index := range []collections.KeySet[uint64]{k.MaintenanceActiveIndex, k.MaintenanceScheduledIndex} {
		iter, err := index.Iterate(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			if len(reservations) >= maxMaintenanceIterationLimit {
				return nil, fmt.Errorf("confirmation maintenance snapshot exceeds %d reservations", maxMaintenanceIterationLimit)
			}
			id, err := iter.Key()
			if err != nil {
				return nil, err
			}
			r, err := k.MaintenanceReservations.Get(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("failed to read indexed maintenance reservation %d: %w", id, err)
			}
			reservations = append(reservations, r)
		}
	}
	return reservations, nil
}

// MaintenanceAddressesForConfirmation resolves the bounded event snapshot.
// Reservations are immutable once active and cannot be canceled after activation,
// so ACTIVE or COMPLETED proves the snapshotted interval actually took effect.
// Scheduled and canceled entries confer no exemption.
func (k Keeper) MaintenanceAddressesForConfirmation(ctx context.Context, event *types.ConfirmationPoCEvent) (map[string]struct{}, error) {
	addresses := make(map[string]struct{})
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if params.EpochParams == nil {
		return nil, fmt.Errorf("epoch params not found")
	}
	generationEnd := event.GetGenerationEnd(params.EpochParams)
	for _, id := range event.MaintenanceReservationIds {
		r, err := k.MaintenanceReservations.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("failed to read maintenance reservation %d: %w", id, err)
		}
		if r.CreatedAtBlockHeight <= 0 || r.CreatedAtBlockHeight >= event.TriggerHeight {
			continue
		}
		endHeight, err := confirmationMaintenanceEnd(r)
		if err != nil {
			return nil, err
		}
		if r.StartHeight > generationEnd || endHeight < event.GenerationStartHeight {
			continue
		}
		if r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE ||
			r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED {
			addresses[r.Participant] = struct{}{}
		}
	}
	return addresses, nil
}

func (k Keeper) checkMaintenanceCancellation(ctx context.Context, height int64, r types.MaintenanceReservation) error {
	event, found, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return err
	}
	// A parameter change must not reopen cancellation for a reservation
	// already counted at the trigger of an in-flight event.
	if found && event != nil && event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED &&
		slices.Contains(event.MaintenanceReservationIds, r.ReservationId) {
		return types.ErrMaintenanceCancellationClosed
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.ConfirmationPocParams == nil || params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch == 0 {
		return nil
	}
	if params.EpochParams == nil {
		return fmt.Errorf("epoch params not found")
	}
	// Lock at the earliest trigger whose generation could reach this start.
	// The trigger block itself is included because its randomness is already
	// known to transactions in that block.
	delay := params.EpochParams.ConfirmationPoCGraceBlocks() - 1
	blocksUntilStart := r.StartHeight - height
	if blocksUntilStart <= delay || blocksUntilStart-delay <= params.EpochParams.PocStageDuration {
		return types.ErrMaintenanceCancellationClosed
	}
	return nil
}

func confirmationMaintenanceEnd(r types.MaintenanceReservation) (int64, error) {
	if r.DurationBlocks == 0 || r.DurationBlocks > math.MaxInt64 || r.StartHeight > math.MaxInt64-int64(r.DurationBlocks) {
		return 0, fmt.Errorf("maintenance reservation %d has invalid range", r.ReservationId)
	}
	return r.StartHeight + int64(r.DurationBlocks) - 1, nil
}
