package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"encoding/binary"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

// SetParticipant set a specific participant in the store from its index
func (k Keeper) SetParticipant(ctx context.Context, participant types.Participant) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))
	key := types.ParticipantKey(participant.Index)

	oldB := store.Get(key)

	b := k.cdc.MustMarshal(&participant)
	store.Set(key, b)

	var oldParticipant *types.Participant
	if oldB == nil {
		oldParticipant = nil
	} else {
		k.cdc.MustUnmarshal(oldB, oldParticipant)
	}

	if !isActiveValidator(oldParticipant) && isActiveValidator(&participant) {
		k.incrementParticipantCounter(ctx)
	} else if isActiveValidator(oldParticipant) && !isActiveValidator(&participant) {
		k.decrementParticipantCounter(ctx)
	}
}

func isActiveValidator(participant *types.Participant) bool {
	if participant == nil {
		return false
	}

	switch participant.Status {
	case types.ParticipantStatus_UNSPECIFIED:
		return false
	case types.ParticipantStatus_ACTIVE:
		return true
	case types.ParticipantStatus_INACTIVE:
		return false
	case types.ParticipantStatus_INVALID:
		return true
	case types.ParticipantStatus_RAMPING:
		return true
	default:
		log.Fatalf("unknown participant status: %v", participant.Status)
	}

	// Effectively unreachable because of the default case
	return false
}

// GetParticipant returns a participant from its index
func (k Keeper) GetParticipant(
	ctx context.Context,
	index string,

) (val types.Participant, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))

	b := store.Get(types.ParticipantKey(
		index,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

func (k Keeper) GetParticipants(ctx context.Context, ids []string) ([]types.Participant, bool) {
	var participants = make([]types.Participant, len(ids))
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))

	for i, id := range ids {
		var p types.Participant
		b := store.Get(types.ParticipantKey(id))

		if b == nil {
			return nil, false
		}

		k.cdc.MustUnmarshal(b, &p)
		participants[i] = p
	}

	return participants, true
}

// RemoveParticipant removes a participant from the store
func (k Keeper) RemoveParticipant(
	ctx context.Context,
	index string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))
	store.Delete(types.ParticipantKey(
		index,
	))
}

// GetAllParticipant returns all participant
func (k Keeper) GetAllParticipant(ctx context.Context) (list []types.Participant) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.Participant
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

func (k Keeper) GetParticipantCounter(ctx context.Context) uint32 {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	b := store.Get(types.ParticipantCounterKey())
	if b == nil {
		return 0
	}

	return binary.BigEndian.Uint32(b)
}

func (k Keeper) setParticipantCounter(counter uint32, ctx context.Context) uint32 {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, counter)

	store.Set(types.ParticipantCounterKey(), b)

	return counter
}

func (k Keeper) incrementParticipantCounter(ctx context.Context) uint32 {
	current := k.GetParticipantCounter(ctx)
	return k.setParticipantCounter(current+1, ctx)
}

func (k Keeper) decrementParticipantCounter(ctx context.Context) uint32 {
	current := k.GetParticipantCounter(ctx)
	return k.setParticipantCounter(current-1, ctx)
}
