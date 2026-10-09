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

// IsRegisteredParticipant reports whether index is a registered participant.
//
// Registration is the weakest predicate that still covers every fee-exempt
// network duty: MessagePermissions gates those handlers on
// ParticipantPermission, ActiveParticipantPermission or an in-handler scan of
// the epoch's BLS participant list, and every ActiveParticipant is built from a
// registered one (module/chainvalidation.go). It is therefore a superset of
// what DeliverTx requires and cannot reject traffic a handler would accept.
//
// Used by NetworkDutyFeeBypassDecorator to decide whether the fee waiver is
// owed, which must not depend on the shorter-lived ActiveParticipantsSet: that
// set only retains ~2 epochs, so gating on it would strip the waiver from
// legitimate previous-epoch claims and from BLS traffic for the epoch being
// rotated. Errors are reported as "not registered" so callers fail closed.
func (k Keeper) IsRegisteredParticipant(ctx context.Context, index string) bool {
	addr, err := sdk.AccAddressFromBech32(index)
	if err != nil {
		return false
	}
	found, err := k.Participants.Has(ctx, addr)
	return err == nil && found
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
	err = k.Participants.Remove(ctx, addr)
	if err != nil {
		k.LogError("Could not remove participant", types.Participants, "error", err, "index", index, "address", addr.String(), "")
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
