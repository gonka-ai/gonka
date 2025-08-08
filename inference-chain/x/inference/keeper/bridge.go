package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// Key prefixes for bridge storage
const (
	BridgeAddressKeyPrefix = "bridge_address:"
)

// Bridge address management functions

// generateBridgeAddressKey creates a unique key for bridge addresses
func (k Keeper) generateBridgeAddressKey(ctx context.Context, chainId string) string {
	return fmt.Sprintf("%s_%d", chainId, len(k.GetBridgeContractAddressesByChain(ctx, chainId))+1)
}

// SetBridgeContractAddress stores a bridge contract address
func (k Keeper) SetBridgeContractAddress(ctx context.Context, address types.BridgeContractAddress) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeAddressKeyPrefix))

	// Generate unique key
	key := k.generateBridgeAddressKey(ctx, address.ChainId)

	// Update the Id field to match our storage key for consistency
	address.Id = key

	bz := k.cdc.MustMarshal(&address)
	store.Set([]byte(key), bz)
}

// GetBridgeContractAddressesByChain retrieves all bridge contract addresses for a specific chain
func (k Keeper) GetBridgeContractAddressesByChain(ctx context.Context, chainId string) []types.BridgeContractAddress {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeAddressKeyPrefix))

	var addresses []types.BridgeContractAddress
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var address types.BridgeContractAddress
		err := k.cdc.Unmarshal(iterator.Value(), &address)
		if err != nil {
			// Log the error but continue processing other addresses
			k.LogError("Bridge exchange: Failed to unmarshal bridge contract address",
				types.Messages,
				"key", string(iterator.Key()),
				"error", err)
			continue
		}
		if address.ChainId == chainId {
			addresses = append(addresses, address)
		}
	}

	return addresses
}

// GetAllBridgeContractAddresses retrieves all bridge contract addresses
func (k Keeper) GetAllBridgeContractAddresses(ctx context.Context) []types.BridgeContractAddress {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeAddressKeyPrefix))

	var addresses []types.BridgeContractAddress
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var address types.BridgeContractAddress
		err := k.cdc.Unmarshal(iterator.Value(), &address)
		if err != nil {
			// Log the error but continue processing other addresses
			k.LogError("Bridge exchange: Failed to unmarshal bridge contract address",
				types.Messages,
				"key", string(iterator.Key()),
				"error", err)
			continue
		}
		addresses = append(addresses, address)
	}

	return addresses
}

// HasBridgeContractAddress checks if a bridge contract address exists for a chain
func (k Keeper) HasBridgeContractAddress(ctx context.Context, chainId, address string) bool {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeAddressKeyPrefix))

	key := k.generateBridgeAddressKey(ctx, chainId)
	return store.Has([]byte(key))
}

// RemoveBridgeContractAddress removes a bridge contract address
func (k Keeper) RemoveBridgeContractAddress(ctx context.Context, chainId, address string) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeAddressKeyPrefix))

	key := k.generateBridgeAddressKey(ctx, chainId)
	store.Delete([]byte(key))
}
