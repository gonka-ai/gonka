package keeper

import (
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetIBCTradeApprovedToken stores an IBC trade approved token
func (k Keeper) SetIBCTradeApprovedToken(ctx sdk.Context, approvedToken types.BridgeTokenReference) error {
	// Validate input data before saving
	if err := k.validateIBCTradeApprovedToken(&approvedToken); err != nil {
		k.LogError("IBC: Failed to save IBC trade approved token - validation failed",
			types.Messages,
			"chainId", approvedToken.ChainId,
			"contractAddress", approvedToken.ContractAddress,
			"error", err)
		return fmt.Errorf("invalid IBC trade approved token data: %w", err)
	}

	// Use lowercase key for consistent collection lookups, but preserve original casing
	// in the stored struct so queries return canonical IBC denom form (e.g. ibc/UPPERCASE_HEX)
	// matching what x/bank and the IBC module use.
	lookupKey := strings.ToLower(approvedToken.ContractAddress)

	if err := k.LiquidityPoolApprovedTokensMap.Set(ctx, collections.Join(approvedToken.ChainId, lookupKey), approvedToken); err != nil {
		k.LogError("IBC: Failed to store IBC trade approved token",
			types.Messages,
			"chainId", approvedToken.ChainId,
			"contractAddress", approvedToken.ContractAddress,
			"error", err)
		return fmt.Errorf("failed to store IBC trade approved token: %w", err)
	}

	k.LogInfo("IBC trade approved token stored",
		types.Messages,
		"chainId", approvedToken.ChainId,
		"contractAddress", approvedToken.ContractAddress)

	return nil
}

// SetIBCTokenMetadata stores metadata for an IBC token
// This is used when x/bank metadata is missing or insufficient (e.g. no decimals)
func (k Keeper) SetIBCTokenMetadata(ctx sdk.Context, chainId, ibcDenom string, metadata types.BridgeTokenMetadata) error {
	// 1. Basic validation
	if strings.TrimSpace(chainId) == "" {
		return fmt.Errorf("chainId cannot be empty")
	}
	if strings.TrimSpace(ibcDenom) == "" {
		return fmt.Errorf("ibcDenom cannot be empty")
	}

	// 2. Validate format using IBC rules (allowing '/')
	// We reuse the logic from validateIBCTradeApprovedToken but applied to the denom
	ref := types.BridgeTokenReference{ChainId: chainId, ContractAddress: ibcDenom}
	if err := k.validateIBCTradeApprovedToken(&ref); err != nil {
		return fmt.Errorf("invalid IBC denom format: %w", err)
	}

	// 3. Use lowercase key for consistent collection lookups, but preserve the original
	// denom casing in the stored struct (canonical IBC form is uppercase hex after ibc/).
	lookupKey := strings.ToLower(ibcDenom)
	metadata.ChainId = chainId
	metadata.ContractAddress = ibcDenom // preserve original casing (e.g. ibc/27394FB...)

	// 4. Update the metadata map
	// We use the SAME map as bridge tokens: WrappedTokenMetadataMap
	// Key is (ChainId, lowercase ContractAddress) for consistent lookups.
	err := k.WrappedTokenMetadataMap.Set(ctx, collections.Join(chainId, lookupKey), metadata)
	if err != nil {
		return fmt.Errorf("failed to set IBC token metadata: %w", err)
	}

	k.LogInfo("IBC token metadata registered",
		types.Messages,
		"chainId", chainId,
		"denom", ibcDenom,
		"symbol", metadata.Symbol,
		"decimals", metadata.Decimals)

	return nil
}

// validateIBCTradeApprovedToken validates the approved token data before saving
func (k Keeper) validateIBCTradeApprovedToken(approvedToken *types.BridgeTokenReference) error {
	if approvedToken == nil {
		return fmt.Errorf("approvedToken cannot be nil")
	}

	// Check for empty or whitespace-only fields
	if strings.TrimSpace(approvedToken.ChainId) == "" {
		return fmt.Errorf("chainId cannot be empty")
	}
	if strings.TrimSpace(approvedToken.ContractAddress) == "" {
		return fmt.Errorf("contractAddress cannot be empty")
	}

	// Check for binary data or control characters
	if containsBinaryData(approvedToken.ChainId) {
		return fmt.Errorf("chainId contains binary data or invalid characters")
	}
	if containsBinaryData(approvedToken.ContractAddress) {
		return fmt.Errorf("contractAddress contains binary data or invalid characters")
	}

	// Validate chain ID format
	if !isValidChainId(approvedToken.ChainId) {
		return fmt.Errorf("invalid chainId format: %s", approvedToken.ChainId)
	}

	// Check for reasonable length limits
	if len(approvedToken.ChainId) > 50 {
		return fmt.Errorf("chainId too long: %d characters", len(approvedToken.ChainId))
	}
	// IBC denoms can be long (e.g. transfer/channel-0/...)
	if len(approvedToken.ContractAddress) > 128 {
		return fmt.Errorf("contractAddress too long: %d characters", len(approvedToken.ContractAddress))
	}

	// Validate contract address format (specialized for IBC tokens to allow '/')
	// Allow alphanumeric, hyphens, underscores, dots, colons, and slashes
	for _, r := range approvedToken.ContractAddress {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ':' || r == '/') {
			return fmt.Errorf("invalid character in IBC contract address: %c", r)
		}
	}

	return nil
}
