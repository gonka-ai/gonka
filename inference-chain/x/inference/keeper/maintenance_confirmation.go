package keeper

import (
	"context"
	"fmt"
	"math"
	"sort"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SnapshotMaintenanceForConfirmation records the existing reservations that may
// excuse absence from this event. It scans only active and scheduled windows,
// not accumulated reservation history. Window lengths come from the reservation,
// so a later reduction of MaintenanceMaxWindowBlocks cannot hide an old window.
//
// The trigger is computed in EndBlock, after transactions. Its randomness is
// already available during that block, so same-block reservations must be
// excluded too. Unknown legacy creation heights do not establish eligibility;
// maintenance must remain disabled until this upgrade on chains without them.
func (k Keeper) SnapshotMaintenanceForConfirmation(ctx context.Context, event *types.ConfirmationPoCEvent) ([]uint64, error) {
	reservations, err := k.confirmationMaintenanceReservations(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for _, r := range reservations {
		if r.CreatedAtBlockHeight <= 0 || r.CreatedAtBlockHeight >= event.TriggerHeight {
			continue
		}
		endHeight, err := confirmationMaintenanceEnd(r)
		if err != nil {
			return nil, err
		}
		// Event phase transitions use live EpochParams. Keep future candidates
		// too, then test the upper bound at evaluation in case governance
		// changes the validation duration while the event is in flight.
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
	validationEnd := event.GetValidationEnd(params.EpochParams)
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
		if r.StartHeight > validationEnd || endHeight < event.GenerationStartHeight {
			continue
		}
		if r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE ||
			r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED {
			addresses[r.Participant] = struct{}{}
		}
	}
	return addresses, nil
}

func confirmationMaintenanceEnd(r types.MaintenanceReservation) (int64, error) {
	if r.DurationBlocks == 0 || r.DurationBlocks > math.MaxInt64 || r.StartHeight > math.MaxInt64-int64(r.DurationBlocks) {
		return 0, fmt.Errorf("maintenance reservation %d has invalid range", r.ReservationId)
	}
	return r.StartHeight + int64(r.DurationBlocks) - 1, nil
}
