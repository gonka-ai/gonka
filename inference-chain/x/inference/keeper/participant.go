package keeper

import (
	"context"
	"encoding/base64"

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
	return k.saveParticipant(ctx, participant)
}

// SetParticipantFromStored is SetParticipant for a participant read earlier in the same tx
// or EndBlock and not written since: storedStats is a copy of its CurrentEpochStats as
// read, so the status check does not read the participant again.
func (k Keeper) SetParticipantFromStored(ctx context.Context, participant types.Participant, storedStats *types.CurrentEpochStats) error {
	return k.setParticipantAsRead(ctx, participant, storedStats, true)
}

// setParticipantAsRead also takes found=false for a participant the caller has just found absent.
func (k Keeper) setParticipantAsRead(ctx context.Context, participant types.Participant, storedStats *types.CurrentEpochStats, found bool) error {
	err := k.updateParticipantStatus(ctx, &participant, storedStats, found)
	if err != nil {
		k.LogError("Failed to update participant status", types.Validation, "error", err)
		return err
	}
	return k.saveParticipant(ctx, participant)
}

func (k Keeper) saveParticipant(ctx context.Context, participant types.Participant) error {
	participantAddress, err := sdk.AccAddressFromBech32(participant.Index)
	if err != nil {
		return err
	}
	err = k.Participants.Set(ctx, participantAddress, storedParticipant(participant, participantAddress))
	if err != nil {
		return err
	}
	k.LogDebug("Saved Participant", types.Participants, "address", participant.Address, "index", participant.Index, "balance", participant.CoinBalance)
	return nil
}

// storedParticipant drops Index and Address, which repeat the key, stores the default
// Weight -1 as absent and canonical base64 32-byte keys as raw bytes; restoredParticipant
// undoes all three. A record that cannot be trimmed unambiguously is stored whole.
func storedParticipant(p types.Participant, addr sdk.AccAddress) types.Participant {
	if p.Index != p.Address || p.Index != addr.String() || p.Weight == 0 {
		return p
	}
	trimmed := p
	trimmed.Index, trimmed.Address = "", ""
	if trimmed.Weight == -1 {
		trimmed.Weight = 0
	}
	var ok bool
	if trimmed.ValidatorKey, ok = rawKey32(p.ValidatorKey); !ok {
		return p
	}
	if trimmed.WorkerPublicKey, ok = rawKey32(p.WorkerPublicKey); !ok {
		return p
	}
	if trimmed.Size() == 0 {
		return p
	}
	return trimmed
}

func restoredParticipant(addr sdk.AccAddress, p types.Participant) types.Participant {
	if p.Index == "" {
		p.Index = addr.String()
		p.Address = p.Index
		if p.Weight == 0 {
			p.Weight = -1
		}
		if len(p.ValidatorKey) == 32 {
			p.ValidatorKey = base64.StdEncoding.EncodeToString([]byte(p.ValidatorKey))
		}
		if len(p.WorkerPublicKey) == 32 {
			p.WorkerPublicKey = base64.StdEncoding.EncodeToString([]byte(p.WorkerPublicKey))
		}
	}
	return p
}

// rawKey32 returns the 32 raw bytes of a canonical base64 key and other values as they are;
// false for a 32-character value, which would read back as raw bytes.
func rawKey32(key string) (string, bool) {
	if raw, err := base64.StdEncoding.DecodeString(key); err == nil && len(raw) == 32 &&
		base64.StdEncoding.EncodeToString(raw) == key {
		return string(raw), true
	}
	return key, len(key) != 32
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
	return restoredParticipant(address, val), true
}

// HasParticipant reports whether index is a stored participant without decoding the record.
func (k Keeper) HasParticipant(ctx context.Context, index string) bool {
	address, err := sdk.AccAddressFromBech32(index)
	if err != nil {
		return false
	}
	found, err := k.Participants.Has(ctx, address)
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
	kvs, err := iter.KeyValues()
	if err != nil {
		return nil
	}
	list = make([]types.Participant, len(kvs))
	for i, kv := range kvs {
		list[i] = restoredParticipant(kv.Key, kv.Value)
	}
	return list
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
