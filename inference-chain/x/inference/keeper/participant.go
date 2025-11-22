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
	err = k.Participants.Set(ctx, participantAddress, participant)
	if err != nil {
		return err
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
	err := k.Participants.Remove(ctx, sdk.MustAccAddressFromBech32(index))
	if err != nil {
		k.LogError("Could not remove participant", types.Participants, "error", err, "index", index, "address", sdk.MustAccAddressFromBech32(index).String(), "")
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

func (k Keeper) CountAllParticipants(ctx context.Context) int64 {
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return 0
	}
	participants, err := iter.Values()
	if err != nil {
		return 0
	}
	return int64(len(participants))
}
