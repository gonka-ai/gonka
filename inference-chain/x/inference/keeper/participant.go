package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

// SetParticipant set a specific participant in the store from its index
func (k Keeper) SetParticipant(ctx context.Context, participant types.Participant) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.ParticipantKeyPrefix))
	key := types.ParticipantKey(participant.Index)

	oldParticipant := k.retrieveParticipant(&store, key)

	b := k.cdc.MustMarshal(&participant)
	store.Set(key, b)
	k.LogDebug("Saved Participant", "address", participant.Address, "index", participant.Index, "balance", participant.CoinBalance)
	group, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogWarn("Failed to get current epoch group", "error", err)
	} else {
		err = group.UpdateMember(ctx, oldParticipant, &participant)
		if err != nil {
			k.LogWarn("Failed to update member", "error", err)
		}
	}
}

func (k Keeper) retrieveParticipant(store *prefix.Store, key []byte) *types.Participant {
	b := store.Get(key)
	if b == nil {
		return nil
	}

	var participant types.Participant
	k.cdc.MustUnmarshal(b, &participant)
	return &participant
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
	key := types.ParticipantKey(index)

	store.Delete(key)
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
