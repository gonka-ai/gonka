package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// Bridge address management functions

// generateBridgeAddressKey builds a deterministic composite identifier: <chainId>/<address>
func (k Keeper) generateBridgeAddressKey(_ context.Context, chainId, address string) string {
	return fmt.Sprintf("%s/%s", chainId, address)
}

// SetBridgeContractAddress stores a bridge contract address
func (k Keeper) SetBridgeContractAddress(ctx context.Context, address types.BridgeContractAddress) {
	address.Id = k.generateBridgeAddressKey(ctx, address.ChainId, address.Address)
	if err := k.BridgeContractAddresses.Set(ctx, collections.Join(address.ChainId, address.Address), address); err != nil {
		k.LogError("Bridge exchange: Failed to set bridge contract address",
			types.Messages,
			"chainId", address.ChainId,
			"address", address.Address,
			"error", err,
		)
	}
}

// GetBridgeContractAddressesByChain retrieves all bridge contract addresses for a specific chain
func (k Keeper) GetBridgeContractAddressesByChain(ctx context.Context, chainId string) []types.BridgeContractAddress {
	iter, err := k.BridgeContractAddresses.Iterate(ctx, collections.NewPrefixedPairRange[string, string](chainId))
	if err != nil {
		k.LogError("Bridge exchange: Failed to iterate bridge contract addresses by chain",
			types.Messages,
			"chainId", chainId,
			"error", err,
		)
		return nil
	}
	defer iter.Close()

	addresses, err := iter.Values()
	if err != nil {
		k.LogError("Bridge exchange: Failed to collect bridge contract addresses by chain",
			types.Messages,
			"chainId", chainId,
			"error", err,
		)
		return nil
	}

	return addresses
}

// GetAllBridgeContractAddresses retrieves all bridge contract addresses
func (k Keeper) GetAllBridgeContractAddresses(ctx context.Context) []types.BridgeContractAddress {
	iter, err := k.BridgeContractAddresses.Iterate(ctx, nil)
	if err != nil {
		k.LogError("Bridge exchange: Failed to iterate bridge contract addresses",
			types.Messages,
			"error", err,
		)
		return nil
	}
	defer iter.Close()

	addresses, err := iter.Values()
	if err != nil {
		k.LogError("Bridge exchange: Failed to collect bridge contract addresses",
			types.Messages,
			"error", err,
		)
		return nil
	}

	return addresses
}

// HasBridgeContractAddress checks if a bridge contract address exists for a chain
func (k Keeper) HasBridgeContractAddress(ctx context.Context, chainId, address string) bool {
	has, err := k.BridgeContractAddresses.Has(ctx, collections.Join(chainId, address))
	if err != nil {
		k.LogError("Bridge exchange: Failed to check bridge contract address",
			types.Messages,
			"chainId", chainId,
			"address", address,
			"error", err,
		)
		return false
	}
	return has
}

// RemoveBridgeContractAddress removes a bridge contract address
func (k Keeper) RemoveBridgeContractAddress(ctx context.Context, chainId, address string) {
	_ = k.BridgeContractAddresses.Remove(ctx, collections.Join(chainId, address))
}
