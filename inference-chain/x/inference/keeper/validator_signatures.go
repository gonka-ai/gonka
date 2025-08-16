package keeper

import (
	"context"
	"errors"
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	validatorSignaturesKeyPrefix = "ValidatorsProof/"
)

func validatorSignaturesFullKey(createAtBlockHeight uint64) []byte {
	var key []byte

	key = append(key, []byte(validatorSignaturesKeyPrefix)...)
	key = append(key, sdk.Uint64ToBigEndian(createAtBlockHeight)...)
	return key
}

func (k Keeper) SetValidatorsSignatures(ctx context.Context, signatures types.ValidatorsProof) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := validatorSignaturesFullKey(uint64(signatures.BlockHeight))

	fmt.Printf("SetValidatorsSignatures: block_height %v, %v\n", signatures.BlockHeight, signatures.Signatures)

	if store.Has(key) {
		return errors.New("validators proof already exists")
	}

	bz := k.cdc.MustMarshal(&signatures)
	store.Set(key, bz)
	return nil
}

func (k Keeper) GetValidatorsSignatures(ctx context.Context, height int64) (types.ValidatorsProof, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	fmt.Printf("GetValidatorsSignatures: block_height %v\n", height)

	key := validatorSignaturesFullKey(uint64(height))
	bz := store.Get(key)
	if bz == nil {
		return types.ValidatorsProof{}, false
	}

	var proof types.ValidatorsProof
	k.cdc.MustUnmarshal(bz, &proof)
	return proof, true
}
