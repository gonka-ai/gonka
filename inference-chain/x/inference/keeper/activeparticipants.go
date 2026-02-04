package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetActiveParticipantsV1(ctx context.Context, participants types.ActiveParticipants) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKeyV1(participants.EpochGroupId)

	b, err := k.cdc.Marshal(&participants)
	if err != nil {
		return err
	}
	store.Set(key, b)
	return nil
}

func (k Keeper) GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(epochId)

	b := store.Get(key)
	if b == nil {
		return types.ActiveParticipants{}, false
	}

	err := k.cdc.Unmarshal(b, &val)
	if err != nil {
		k.LogError("Unable to marshal ActiveParticipants", types.Participants, "epochIndex", epochId)
		return types.ActiveParticipants{}, false
	}
	return val, true
}

func (k Keeper) SetActiveParticipants(ctx context.Context, participants types.ActiveParticipants) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(participants.EpochId)

	b, err := k.cdc.Marshal(&participants)
	if err != nil {
		return err
	}
	store.Set(key, b)
	return nil
}
