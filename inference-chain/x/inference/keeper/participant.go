package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetParticipant set a specific participant in the store from its index
func (k Keeper) SetParticipant(ctx context.Context, participant types.Participant) {
	participantAddress := sdk.MustAccAddressFromBech32(participant.Index)
	var oldParticipant *types.Participant
	p, err := k.Participants.Get(ctx, participantAddress)
	if err != nil {
		oldParticipant = nil
	} else {
		oldParticipant = &p
	}
	err = k.Participants.Set(ctx, participantAddress, participant)
	if err != nil {
		panic(err)
	}
	k.LogDebug("Saved Participant", types.Participants, "address", participant.Address, "index", participant.Index, "balance", participant.CoinBalance)
	group, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogWarn("Failed to get current epoch group", types.Participants, "error", err)
	} else {
		err = group.UpdateMember(ctx, oldParticipant, &participant)
		if err != nil {
			k.LogWarn("Failed to update member", types.Participants, "error", err)
		}
	}
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

func (k Keeper) GetParticipants(ctx context.Context, ids []string) ([]types.Participant, bool) {
	var participants = make([]types.Participant, len(ids))
	for i, id := range ids {
		b, err := k.Participants.Get(ctx, sdk.MustAccAddressFromBech32(id))
		if err != nil {
			return nil, false
		}
		participants[i] = b
	}

	return participants, true
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

func (k Keeper) CountAllParticipantsWithNotZeroBalance(ctx context.Context) int64 {
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
