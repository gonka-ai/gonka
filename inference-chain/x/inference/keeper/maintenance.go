package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// --- Reservation CRUD ---

// NextMaintenanceReservationID returns the next reservation ID and increments the counter.
func (k Keeper) NextMaintenanceReservationID(ctx context.Context) (uint64, error) {
	counter, err := k.MaintenanceReservationCounter.Get(ctx)
	if err != nil {
		counter = 0
	}
	nextID := counter + 1
	if err := k.MaintenanceReservationCounter.Set(ctx, nextID); err != nil {
		return 0, err
	}
	return nextID, nil
}

// SetMaintenanceReservation stores a reservation by its ID.
func (k Keeper) SetMaintenanceReservation(ctx context.Context, r types.MaintenanceReservation) error {
	return k.MaintenanceReservations.Set(ctx, r.ReservationId, r)
}

// GetMaintenanceReservation retrieves a reservation by ID.
func (k Keeper) GetMaintenanceReservation(ctx context.Context, id uint64) (types.MaintenanceReservation, bool) {
	v, err := k.MaintenanceReservations.Get(ctx, id)
	if err != nil {
		return types.MaintenanceReservation{}, false
	}
	return v, true
}

// --- MaintenanceState CRUD ---

// SetMaintenanceState stores the per-participant maintenance state.
func (k Keeper) SetMaintenanceState(ctx context.Context, state types.MaintenanceState) error {
	addr, err := sdk.AccAddressFromBech32(state.Participant)
	if err != nil {
		return err
	}
	return k.MaintenanceStates.Set(ctx, addr, state)
}

// GetMaintenanceState retrieves per-participant maintenance state.
func (k Keeper) GetMaintenanceState(ctx context.Context, participant sdk.AccAddress) (types.MaintenanceState, bool) {
	v, err := k.MaintenanceStates.Get(ctx, participant)
	if err != nil {
		return types.MaintenanceState{}, false
	}
	return v, true
}

// GetOrCreateMaintenanceState retrieves or initializes maintenance state for a participant.
func (k Keeper) GetOrCreateMaintenanceState(ctx context.Context, participant sdk.AccAddress) types.MaintenanceState {
	state, found := k.GetMaintenanceState(ctx, participant)
	if !found {
		return types.MaintenanceState{
			Participant: participant.String(),
		}
	}
	return state
}

// --- Transition Schedule ---

// SetMaintenanceTransition stores a transition entry for exact block-height lookup in BeginBlock.
// transitionType: 1 = activate, 2 = complete (maps to MaintenanceTransitionType enum values).
func (k Keeper) SetMaintenanceTransition(ctx context.Context, blockHeight int64, reservationID uint64, transitionType uint32) error {
	return k.MaintenanceTransitions.Set(ctx, collections.Join(blockHeight, reservationID), transitionType)
}

// DeleteMaintenanceTransition removes a consumed transition entry.
func (k Keeper) DeleteMaintenanceTransition(ctx context.Context, blockHeight int64, reservationID uint64) error {
	return k.MaintenanceTransitions.Remove(ctx, collections.Join(blockHeight, reservationID))
}

// IterateMaintenanceTransitionsAtHeight iterates over all transitions scheduled for the exact given height.
func (k Keeper) IterateMaintenanceTransitionsAtHeight(ctx context.Context, blockHeight int64, fn func(reservationID uint64, transitionType uint32) (stop bool, err error)) error {
	rng := collections.NewPrefixedPairRange[int64, uint64](blockHeight)
	iter, err := k.MaintenanceTransitions.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return err
		}
		reservationID := kv.Key.K2()
		transitionType := kv.Value
		stop, err := fn(reservationID, transitionType)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// --- Start Height Index (for scheduling overlap checks) ---

// SetMaintenanceStartHeightIndex adds an index entry for a reservation by its start height.
func (k Keeper) SetMaintenanceStartHeightIndex(ctx context.Context, startHeight int64, reservationID uint64) error {
	return k.MaintenanceStartHeightIndex.Set(ctx, collections.Join(startHeight, reservationID), reservationID)
}

// DeleteMaintenanceStartHeightIndex removes a start-height index entry.
func (k Keeper) DeleteMaintenanceStartHeightIndex(ctx context.Context, startHeight int64, reservationID uint64) error {
	return k.MaintenanceStartHeightIndex.Remove(ctx, collections.Join(startHeight, reservationID))
}

// IterateMaintenanceStartHeightRange iterates reservations whose start height falls in [fromHeight, toHeight].
// Used for bounded overlap checks during scheduling.
func (k Keeper) IterateMaintenanceStartHeightRange(ctx context.Context, fromHeight, toHeight int64, fn func(reservationID uint64) (stop bool, err error)) error {
	for h := fromHeight; h <= toHeight; h++ {
		err := k.iterateStartHeightPrefix(ctx, h, fn)
		if err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) iterateStartHeightPrefix(ctx context.Context, height int64, fn func(reservationID uint64) (stop bool, err error)) error {
	rng := collections.NewPrefixedPairRange[int64, uint64](height)
	iter, err := k.MaintenanceStartHeightIndex.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return err
		}
		reservationID := kv.Value
		stop, err := fn(reservationID)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// --- Convenience: check if a participant is in active maintenance ---

// IsParticipantInActiveMaintenance returns true if the participant has an active maintenance window.
func (k Keeper) IsParticipantInActiveMaintenance(ctx context.Context, participant sdk.AccAddress) bool {
	state, found := k.GetMaintenanceState(ctx, participant)
	if !found {
		return false
	}
	if state.ActiveReservationId == 0 {
		return false
	}
	r, found := k.GetMaintenanceReservation(ctx, state.ActiveReservationId)
	if !found {
		return false
	}
	return r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
}
