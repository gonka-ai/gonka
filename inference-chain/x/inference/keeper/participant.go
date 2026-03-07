package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetParticipant set a specific participant in the store from its index
func (k Keeper) SetParticipant(ctx context.Context, participant types.Participant) error {
	// Compute new status and delegate transition handling to a unified method.
	err := k.UpdateParticipantStatus(ctx, &participant)
	if err != nil {
		k.LogError("Failed to update participant status", types.Validation, "error", err)
		return err
	}

	participantAddress, err := sdk.AccAddressFromBech32(participant.Index)
	if err != nil {
		return err
	}

	// Check if this is a new participant (for counter maintenance)
	isNew, _ := k.Participants.Has(ctx, participantAddress)

	err = k.Participants.Set(ctx, participantAddress, participant)
	if err != nil {
		return err
	}

	// Increment participant counter only for new participants
	if !isNew {
		k.incrementParticipantCount(ctx)
	}

	k.LogDebug("Saved Participant", types.Participants, "address", participant.Address, "index", participant.Index, "balance", participant.CoinBalance)
	return nil
}

func (k Keeper) GetParticipants(
	ctx context.Context,
	addresses []string) (participants []types.Participant) {
	for _, address := range addresses {
		participant, found := k.GetParticipant(ctx, address)
		if found {
			participants = append(participants, participant)
		}
	}
	return participants
}

// GetParticipant returns a participant from its index
func (k Keeper) GetParticipant(
	ctx context.Context,
	index string,
) (val types.Participant, found bool) {
	address, err := sdk.AccAddressFromBech32(index)
	if err != nil {
		k.LogError("Could not parse participant address", types.Participants, "address", index, "error", err)
		return val, false
	}
	val, err = k.Participants.Get(ctx, address)
	if err != nil {
		return val, false
	}
	return val, true
}

// RemoveParticipant removes a participant from the store
func (k Keeper) RemoveParticipant(
	ctx context.Context,
	index string,
) {
	addr, err := sdk.AccAddressFromBech32(index)
	if err != nil {
		k.LogError("Could not parse participant address for removal", types.Participants, "index", index, "error", err)
		return
	}

	// Check if participant exists before removing (for counter maintenance)
	exists, _ := k.Participants.Has(ctx, addr)

	err = k.Participants.Remove(ctx, addr)
	if err != nil {
		k.LogError("Could not remove participant", types.Participants, "error", err, "index", index, "address", addr.String(), "")
		return
	}

	// Decrement participant counter only if participant existed
	if exists {
		k.decrementParticipantCount(ctx)
	}
}

// GetAllParticipant returns all participant
func (k Keeper) GetAllParticipant(ctx context.Context) (list []types.Participant) {
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	participants, err := iter.Values()
	if err != nil {
		return nil
	}
	return participants
}

// CountAllParticipants returns the participant count from the store counter.
// Falls back to key-only iteration if the counter has not been initialized yet,
// and initializes the counter as a side effect.
func (k Keeper) CountAllParticipants(ctx context.Context) int64 {
	count, err := k.ParticipantCountItem.Get(ctx)
	if err == nil && count >= 0 {
		return count
	}

	// Counter not initialized — fall back to iteration and initialize it
	count = k.countAllParticipantsByIteration(ctx)
	_ = k.ParticipantCountItem.Set(ctx, count)
	return count
}

// countAllParticipantsByIteration counts participants by iterating keys only,
// without deserializing all participant data into memory.
func (k Keeper) countAllParticipantsByIteration(ctx context.Context) int64 {
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return 0
	}
	defer iter.Close()
	count := int64(0)
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count
}

// incrementParticipantCount atomically increments the participant counter in the store.
func (k Keeper) incrementParticipantCount(ctx context.Context) {
	count, err := k.ParticipantCountItem.Get(ctx)
	if err != nil {
		// Counter not initialized — initialize from iteration
		count = k.countAllParticipantsByIteration(ctx)
		_ = k.ParticipantCountItem.Set(ctx, count)
		return
	}
	_ = k.ParticipantCountItem.Set(ctx, count+1)
}

// decrementParticipantCount atomically decrements the participant counter in the store.
func (k Keeper) decrementParticipantCount(ctx context.Context) {
	count, err := k.ParticipantCountItem.Get(ctx)
	if err != nil {
		// Counter not initialized — initialize from iteration
		count = k.countAllParticipantsByIteration(ctx)
		_ = k.ParticipantCountItem.Set(ctx, count)
		return
	}
	if count > 0 {
		_ = k.ParticipantCountItem.Set(ctx, count-1)
	}
}
