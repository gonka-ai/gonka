package keeper

import (
	"context"
	"errors"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	blockProofKeyPrefix   = "BlockProof/"
	pendingProofKeyPrefix = "PendingProof/"
)

func blockProofFullKey(createAtBlockHeight uint64) []byte {
	var key []byte

	key = append(key, []byte(blockProofKeyPrefix)...)
	key = append(key, sdk.Uint64ToBigEndian(createAtBlockHeight)...)
	return key
}

func pendingProofKey(height uint64) []byte {
	var key []byte
	key = append(key, []byte(pendingProofKeyPrefix)...)
	key = append(key, sdk.Uint64ToBigEndian(height)...)
	return key
}

func (k Keeper) SetBlockProof(ctx context.Context, proof types.BlockProof) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := blockProofFullKey(uint64(proof.CreatedAtBlockHeight))

	if store.Has(key) {
		return errors.New("block proof already exists")
	}

	bz := k.cdc.MustMarshal(&proof)
	store.Set(key, bz)
	return nil
}

func (k Keeper) GetBlockProof(ctx context.Context, height int64) (types.BlockProof, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := blockProofFullKey(uint64(height))
	bz := store.Get(key)
	if bz == nil {
		return types.BlockProof{}, false
	}

	var proof types.BlockProof
	k.cdc.MustUnmarshal(bz, &proof)
	return proof, true
}

func (k Keeper) SetPendingProof(ctx context.Context, height int64, participantsEpoch uint64) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(pendingProofKey(uint64(height)), sdk.Uint64ToBigEndian(participantsEpoch))
}

func (k Keeper) GetPendingProof(ctx context.Context, height int64) (uint64, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	bz := store.Get(pendingProofKey(uint64(height)))
	if bz == nil {
		return 0, false
	}

	epochId := sdk.BigEndianToUint64(bz)
	return epochId, true
}

func (k Keeper) ClearPendingProof(ctx context.Context, height int64) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(pendingProofKey(uint64(height)))
}
