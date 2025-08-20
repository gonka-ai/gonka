package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetActiveParticipantsV1(ctx context.Context, participants types.ActiveParticipants) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKeyV1(participants.EpochGroupId)

	b := k.cdc.MustMarshal(&participants)
	store.Set(key, b)
}

func (k Keeper) GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(epochId)

	b := store.Get(key)
	if b == nil {
		return types.ActiveParticipants{}, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

func (k Keeper) SetActiveParticipants(ctx context.Context, participants types.ActiveParticipants) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(participants.EpochId)

	b := k.cdc.MustMarshal(&participants)
	store.Set(key, b)
}

func (k Keeper) SetActiveParticipantsProof(ctx context.Context, proof types.ProofOps, blockHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsProofFullKey(blockHeight)
	b := k.cdc.MustMarshal(&proof)
	store.Set(key, b)
}

func (k Keeper) GetActiveParticipantsProof(ctx context.Context, blockHeight int64) (types.ProofOps, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsProofFullKey(uint64(blockHeight))
	b := store.Get(key)
	if b == nil {
		return types.ProofOps{}, false
	}

	var val types.ProofOps
	k.cdc.MustUnmarshal(b, &val)
	return val, true
}
