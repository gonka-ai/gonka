package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetConfirmationPoCEvent stores a confirmation PoC event
func (k Keeper) SetConfirmationPoCEvent(ctx context.Context, event types.ConfirmationPoCEvent) error {
	pk := collections.Join(event.EpochIndex, event.EventSequence)
	return k.ConfirmationPoCEvents.Set(ctx, pk, event)
}

// GetConfirmationPoCEvent retrieves a confirmation PoC event by epoch index and event sequence
func (k Keeper) GetConfirmationPoCEvent(ctx context.Context, epochIndex uint64, eventSequence uint64) (types.ConfirmationPoCEvent, bool, error) {
	pk := collections.Join(epochIndex, eventSequence)
	event, err := k.ConfirmationPoCEvents.Get(ctx, pk)
	if err != nil {
		return types.ConfirmationPoCEvent{}, false, err
	}
	return event, true, nil
}

// GetActiveConfirmationPoCEvent retrieves the currently active confirmation PoC event (if any)
func (k Keeper) GetActiveConfirmationPoCEvent(ctx context.Context) (*types.ConfirmationPoCEvent, bool, error) {
	// Check if active event exists
	has, err := k.ActiveConfirmationPoCEventItem.Has(ctx)
	if err != nil {
		return nil, false, err
	}
	if !has {
		return nil, false, nil // No active event is normal
	}
	
	// Get the active event
	event, err := k.ActiveConfirmationPoCEventItem.Get(ctx)
	if err != nil {
		return nil, false, err
	}
	return &event, true, nil
}

// SetActiveConfirmationPoCEvent sets the currently active confirmation PoC event
func (k Keeper) SetActiveConfirmationPoCEvent(ctx context.Context, event types.ConfirmationPoCEvent) error {
	return k.ActiveConfirmationPoCEventItem.Set(ctx, event)
}

// ClearActiveConfirmationPoCEvent clears the currently active confirmation PoC event
func (k Keeper) ClearActiveConfirmationPoCEvent(ctx context.Context) error {
	return k.ActiveConfirmationPoCEventItem.Remove(ctx)
}

// GetAllConfirmationPoCEventsForEpoch retrieves all confirmation PoC events for a given epoch
func (k Keeper) GetAllConfirmationPoCEventsForEpoch(ctx context.Context, epochIndex uint64) ([]types.ConfirmationPoCEvent, error) {
	it, err := k.ConfirmationPoCEvents.Iterate(ctx, collections.NewPrefixedPairRange[uint64, uint64](epochIndex))
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var events []types.ConfirmationPoCEvent
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil, err
		}
		events = append(events, v)
	}
	return events, nil
}
