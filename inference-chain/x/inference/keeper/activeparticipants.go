package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetActiveParticipants(ctx context.Context, participants types.ActiveParticipants) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(participants.EpochGroupId)

	b := k.cdc.MustMarshal(&participants)
	store.Set(key, b)
}

func (k Keeper) GetActiveParticipants(ctx context.Context, epoch uint64) (val types.ActiveParticipants, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(epoch)

	b := store.Get(key)
	if b == nil {
		return types.ActiveParticipants{}, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

func (k Keeper) GetParticipantCounter(ctx context.Context, epoch uint64) uint32 {
	activeParticipants, ok := k.GetActiveParticipants(ctx, epoch)
	if !ok {
		return 0
	}
	return uint32(len(activeParticipants.Participants))
}
