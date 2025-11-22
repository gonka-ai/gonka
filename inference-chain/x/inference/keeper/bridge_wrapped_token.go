package keeper

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"cosmossdk.io/collections"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// BridgeTokenInstantiateMsg represents the JSON message used to instantiate bridge token contract
// Note: name, symbol and decimals are not included as they will be queried from chain metadata
type BridgeTokenInstantiateMsg struct {
	ChainId         string     `json:"chain_id"`
	ContractAddress string     `json:"contract_address"`
	InitialBalances []Balance  `json:"initial_balances"`
	Mint            *MintInfo  `json:"mint,omitempty"`
	Marketing       *Marketing `json:"marketing,omitempty"`
	Admin           *string    `json:"admin,omitempty"`
}

type Balance struct {
	Address string `json:"address"`
	Amount  string `json:"amount"`
}

type MintInfo struct {
	Minter string `json:"minter"`
}

type Marketing struct {
	Project     string `json:"project,omitempty"`
	Description string `json:"description,omitempty"`
	Marketing   string `json:"marketing,omitempty"`
	Logo        string `json:"logo,omitempty"`
}

// Precompiled regex for Ethereum addresses: 0x + 40 hex chars (case-insensitive)
var eth40HexRegex = regexp.MustCompile(`^(?i)0x[0-9a-f]{40}$`)

// TokenMetadata represents additional token metadata that can be stored in chain state
type TokenMetadata struct {
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Decimals  uint8  `json:"decimals"`
	Overwrite bool   `json:"overwrite,omitempty"` // If false and metadata exists, operation will fail
}

// SetTokenMetadata stores additional token metadata in chain state
func (k Keeper) SetTokenMetadata(ctx sdk.Context, externalChain, externalContract string, metadata TokenMetadata) error {
	// Validate input parameters
	if err := k.validateTokenMetadataInputs(externalChain, externalContract, &metadata); err != nil {
		k.LogError("Bridge exchange: Failed to save token metadata - validation failed",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"error", err)
		return fmt.Errorf("invalid token metadata: %w", err)
	}

	normalizedContract := strings.ToLower(externalContract)
	key := collections.Join(externalChain, normalizedContract)

	storageMetadata := types.BridgeTokenMetadata{
		ChainId:         externalChain,
		ContractAddress: normalizedContract,
		Name:            metadata.Name,
		Symbol:          metadata.Symbol,
		Decimals:        uint32(metadata.Decimals),
	}

	existing, err := k.WrappedTokenMetadataMap.Get(ctx, key)
	if err == nil {
		if !metadata.Overwrite {
			return fmt.Errorf("token metadata already exists for chain %s contract %s and overwrite is false", externalChain, externalContract)
		}
		k.LogInfo("Bridge exchange: Overwriting existing token metadata",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"oldName", existing.Name,
			"newName", metadata.Name,
			"oldSymbol", existing.Symbol,
			"newSymbol", metadata.Symbol,
			"oldDecimals", existing.Decimals,
			"newDecimals", metadata.Decimals)
	} else if !errors.Is(err, collections.ErrNotFound) {
		k.LogError("Bridge exchange: Failed to fetch existing token metadata",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"error", err)
		return err
	}

	if err := k.WrappedTokenMetadataMap.Set(ctx, key, storageMetadata); err != nil {
		k.LogError("Bridge exchange: Failed to store token metadata",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"error", err)
		return err
	}

	k.LogInfo("Bridge exchange: Token metadata stored",
		types.Messages,
		"chain", externalChain,
		"contract", externalContract,
		"name", metadata.Name,
		"symbol", metadata.Symbol,
		"decimals", metadata.Decimals,
		"overwrite", metadata.Overwrite)

	return nil
}

// validateTokenMetadataInputs validates token metadata before saving
func (k Keeper) validateTokenMetadataInputs(externalChain, externalContract string, metadata *TokenMetadata) error {
	// Validate chain and contract parameters
	if strings.TrimSpace(externalChain) == "" {
		return fmt.Errorf("externalChain cannot be empty")
	}
	if strings.TrimSpace(externalContract) == "" {
		return fmt.Errorf("externalContract cannot be empty")
	}

	// Check for binary data in input parameters
	if containsBinaryData(externalChain) {
		return fmt.Errorf("externalChain contains binary data or invalid characters")
	}
	if containsBinaryData(externalContract) {
		return fmt.Errorf("externalContract contains binary data or invalid characters")
	}

	// Validate chain ID format
	if !isValidChainId(externalChain) {
		return fmt.Errorf("invalid externalChain format: %s", externalChain)
	}

	// Validate contract address format
	if !isValidContractAddress(externalContract) {
		return fmt.Errorf("invalid externalContract format: %s", externalContract)
	}

	// Validate metadata fields
	if metadata == nil {
		return fmt.Errorf("metadata cannot be nil")
	}

	// Check for binary data in metadata fields
	if containsBinaryData(metadata.Name) {
		return fmt.Errorf("metadata.Name contains binary data or invalid characters")
	}
	if containsBinaryData(metadata.Symbol) {
		return fmt.Errorf("metadata.Symbol contains binary data or invalid characters")
	}

	// Validate metadata content
	if strings.TrimSpace(metadata.Name) == "" {
		return fmt.Errorf("metadata.Name cannot be empty")
	}
	if strings.TrimSpace(metadata.Symbol) == "" {
		return fmt.Errorf("metadata.Symbol cannot be empty")
	}

	// Check reasonable length limits
	if len(metadata.Name) > 100 {
		return fmt.Errorf("metadata.Name too long: %d characters", len(metadata.Name))
	}
	if len(metadata.Symbol) > 20 {
		return fmt.Errorf("metadata.Symbol too long: %d characters", len(metadata.Symbol))
	}

	// Validate decimals (should be 0-18 for most tokens)
	if metadata.Decimals > 18 {
		return fmt.Errorf("metadata.Decimals too high: %d (max 18)", metadata.Decimals)
	}

	return nil
}

// SetTokenMetadataAndUpdateContract stores token metadata and updates the wrapped token contract if it exists
// This function should only be called from governance proposals
func (k Keeper) SetTokenMetadataAndUpdateContract(ctx sdk.Context, externalChain, externalContract string, metadata TokenMetadata) error {
	// First, store the metadata
	err := k.SetTokenMetadata(ctx, externalChain, externalContract, metadata)
	if err != nil {
		return err
	}

	// Then, update the wrapped token contract if it exists
	if existingContract, found := k.GetWrappedTokenContract(ctx, externalChain, externalContract); found {
		// Update the wrapped token contract with the new metadata
		err := k.updateWrappedTokenContractMetadata(ctx, existingContract.WrappedContractAddress, metadata)
		if err != nil {
			k.LogError("Bridge exchange: Failed to update wrapped token contract metadata", types.Messages, "error", err)
			// Don't fail the entire operation, just log the error
		} else {
			k.LogInfo("Bridge exchange: Wrapped token contract metadata was updated",
				types.Messages,
				"chain", externalChain,
				"contract", externalContract,
				"wrappedContract", existingContract.WrappedContractAddress,
				"name", metadata.Name,
				"symbol", metadata.Symbol,
				"decimals", metadata.Decimals)
		}
	}

	return nil
}

// GetTokenMetadata retrieves token metadata from chain state
func (k Keeper) GetTokenMetadata(ctx sdk.Context, externalChain, externalContract string) (TokenMetadata, bool) {
	normalizedContract := strings.ToLower(externalContract)
	metadata, err := k.WrappedTokenMetadataMap.Get(ctx, collections.Join(externalChain, normalizedContract))
	if err != nil {
		return TokenMetadata{}, false
	}

	return TokenMetadata{
		Name:     metadata.Name,
		Symbol:   metadata.Symbol,
		Decimals: uint8(metadata.Decimals),
	}, true
}

// SetWrappedTokenContract stores a token contract mapping
func (k Keeper) SetWrappedTokenContract(ctx sdk.Context, contract types.BridgeWrappedTokenContract) {
	// Validate input data before saving
	if err := k.validateBridgeWrappedTokenContract(&contract); err != nil {
		k.LogError("Bridge exchange: Failed to save wrapped token contract - validation failed",
			types.Messages,
			"chainId", contract.ChainId,
			"contractAddress", contract.ContractAddress,
			"wrappedContractAddress", contract.WrappedContractAddress,
			"error", err)
		panic(fmt.Sprintf("invalid wrapped token contract data: %v", err))
	}

	normalizedContract := strings.ToLower(contract.ContractAddress)
	normalizedWrapped := strings.ToLower(contract.WrappedContractAddress)

	contract.ContractAddress = normalizedContract
	contract.WrappedContractAddress = normalizedWrapped

	if err := k.WrappedTokenContractsMap.Set(ctx, collections.Join(contract.ChainId, normalizedContract), contract); err != nil {
		panic(fmt.Sprintf("failed to store wrapped token contract: %v", err))
	}

	reference := types.BridgeTokenReference{
		ChainId:         contract.ChainId,
		ContractAddress: normalizedContract,
	}

	if err := k.WrappedContractReverseIndex.Set(ctx, normalizedWrapped, reference); err != nil {
		panic(fmt.Sprintf("failed to store wrapped contract reverse index: %v", err))
	}

	k.LogInfo("Bridge exchange: Wrapped token contract stored successfully",
		types.Messages,
		"chainId", contract.ChainId,
		"contractAddress", contract.ContractAddress,
		"wrappedContractAddress", contract.WrappedContractAddress)
}

// validateBridgeWrappedTokenContract validates the contract data before saving
func (k Keeper) validateBridgeWrappedTokenContract(contract *types.BridgeWrappedTokenContract) error {
	if contract == nil {
		return fmt.Errorf("contract cannot be nil")
	}

	// Check for empty or whitespace-only fields
	if strings.TrimSpace(contract.ChainId) == "" {
		return fmt.Errorf("chainId cannot be empty")
	}
	if strings.TrimSpace(contract.ContractAddress) == "" {
		return fmt.Errorf("contractAddress cannot be empty")
	}
	if strings.TrimSpace(contract.WrappedContractAddress) == "" {
		return fmt.Errorf("wrappedContractAddress cannot be empty")
	}

	// Check for binary data or control characters
	if containsBinaryData(contract.ChainId) {
		return fmt.Errorf("chainId contains binary data or invalid characters")
	}
	if containsBinaryData(contract.ContractAddress) {
		return fmt.Errorf("contractAddress contains binary data or invalid characters")
	}
	if containsBinaryData(contract.WrappedContractAddress) {
		return fmt.Errorf("wrappedContractAddress contains binary data or invalid characters")
	}

	// Validate chain ID format (should be alphanumeric with possible hyphens/underscores)
	if !isValidChainId(contract.ChainId) {
		return fmt.Errorf("invalid chainId format: %s", contract.ChainId)
	}

	// Validate contract addresses (should be hex addresses)
	if !isValidContractAddress(contract.ContractAddress) {
		return fmt.Errorf("invalid contractAddress format: %s", contract.ContractAddress)
	}
	if !isValidContractAddress(contract.WrappedContractAddress) {
		return fmt.Errorf("invalid wrappedContractAddress format: %s", contract.WrappedContractAddress)
	}

	// Check for reasonable length limits
	if len(contract.ChainId) > 50 {
		return fmt.Errorf("chainId too long: %d characters", len(contract.ChainId))
	}
	if len(contract.ContractAddress) > 100 {
		return fmt.Errorf("contractAddress too long: %d characters", len(contract.ContractAddress))
	}
	if len(contract.WrappedContractAddress) > 100 {
		return fmt.Errorf("wrappedContractAddress too long: %d characters", len(contract.WrappedContractAddress))
	}

	return nil
}

// isValidChainId validates chain ID format
func isValidChainId(chainId string) bool {
	// Chain ID should be alphanumeric with possible hyphens/underscores
	for _, r := range chainId {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// isValidContractAddress validates contract address format
func isValidContractAddress(address string) bool {
	// Check for empty or whitespace-only address
	if strings.TrimSpace(address) == "" {
		return false
	}

	// Check for binary data or control characters
	if containsBinaryData(address) {
		return false
	}

	// Check reasonable length (most blockchain addresses are between 26-128 characters)
	if len(address) < 10 || len(address) > 128 {
		return false
	}

	// Allow various address formats:
	// - Ethereum: 0x + 40 hex chars (42 total)
	// - Cosmos: bech32 format (cosmos1..., osmo1..., etc.)
	// - Other chains: various formats

	// For Ethereum-style addresses, validate hex format via regex
	if eth40HexRegex.MatchString(address) {
		return true
	}

	// For other formats, just check for reasonable characters
	// Allow alphanumeric, hyphens, underscores, and dots
	for _, r := range address {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}

	return true
}

// GetWrappedTokenContract retrieves a token contract mapping
func (k Keeper) GetWrappedTokenContract(ctx sdk.Context, externalChain, externalContract string) (types.BridgeWrappedTokenContract, bool) {
	contract, err := k.WrappedTokenContractsMap.Get(ctx, collections.Join(externalChain, strings.ToLower(externalContract)))
	if err != nil {
		return types.BridgeWrappedTokenContract{}, false
	}
	return contract, true
}

// GetWrappedTokenContractByWrappedAddress retrieves a wrapped token contract by its wrapped contract address
func (k Keeper) GetWrappedTokenContractByWrappedAddress(ctx sdk.Context, wrappedContractAddress string) (types.BridgeWrappedTokenContract, bool) {
	reference, err := k.WrappedContractReverseIndex.Get(ctx, strings.ToLower(wrappedContractAddress))
	if err != nil {
		return types.BridgeWrappedTokenContract{}, false
	}
	return k.GetWrappedTokenContract(ctx, reference.ChainId, reference.ContractAddress)
}

func (k Keeper) GetWrappedTokenCodeID(ctx sdk.Context) (uint64, bool) {
	codeID, err := k.WrappedTokenCodeIDItem.Get(ctx)
	if err != nil {
		return 0, false
	}
	return codeID, true
}

func (k Keeper) SetWrappedTokenCodeID(ctx sdk.Context, codeID uint64) {
	if err := k.WrappedTokenCodeIDItem.Set(ctx, codeID); err != nil {
		panic(fmt.Sprintf("failed to set wrapped token code id: %v", err))
	}
}

// ClearWrappedTokenCodeID removes the stored wrapped token code ID from state.
// Returns true if the value existed and was deleted.
func (k Keeper) ClearWrappedTokenCodeID(ctx sdk.Context) bool {
	if err := k.WrappedTokenCodeIDItem.Remove(ctx); err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false
		}
		k.LogError("Bridge exchange: Failed to clear wrapped token code ID", types.Messages, "error", err)
		return false
	}
	return true
}

// MigrateAllWrappedTokenContracts migrates all known wrapped token contract instances to the given code ID.
// The module account is the admin of these instances, so it can invoke Migrate.
// migrateMsg can be nil or an empty JSON object when no special migration data is needed.
func (k Keeper) MigrateAllWrappedTokenContracts(ctx sdk.Context, newCodeID uint64, migrateMsg json.RawMessage) error {
	permissionedKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())
	adminAddr := k.AccountKeeper.GetModuleAddress(types.ModuleName)
	if len(migrateMsg) == 0 {
		migrateMsg = json.RawMessage([]byte("{}"))
	}

	iter, err := k.WrappedTokenContractsMap.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	contracts, err := iter.Values()
	if err != nil {
		return err
	}

	var firstErr error
	for _, contract := range contracts {
		wrappedAddr := contract.WrappedContractAddress
		// Execute migrate on the contract
		_, err := permissionedKeeper.Migrate(
			ctx,
			sdk.MustAccAddressFromBech32(wrappedAddr),
			adminAddr,
			newCodeID,
			migrateMsg,
		)
		if err != nil {
			// Record first error but continue migrating others
			k.LogError("Bridge exchange: Failed to migrate wrapped token contract",
				types.Messages,
				"wrappedContract", wrappedAddr,
				"newCodeID", newCodeID,
				"error", err,
			)
			if firstErr == nil {
				firstErr = fmt.Errorf("migrate %s: %w", wrappedAddr, err)
			}
			continue
		}

		k.LogInfo("Bridge exchange: Migrated wrapped token contract",
			types.Messages,
			"wrappedContract", wrappedAddr,
			"newCodeID", newCodeID,
		)
	}

	return firstErr
}

func (k Keeper) GetOrCreateWrappedTokenContract(ctx sdk.Context, chainId, contractAddress string) (string, error) {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())
	// Check if mapping already exists
	contract, found := k.GetWrappedTokenContract(ctx, chainId, contractAddress)
	if found {
		return contract.WrappedContractAddress, nil
	}

	// Get the stored wrapped token code ID
	codeID, found := k.GetWrappedTokenCodeID(ctx)
	if !found {
		return "", fmt.Errorf("CW20 code ID not found")
	}

	// Prepare instantiate message for bridge token contract
	// Note: name, symbol and decimals will be queried from chain metadata by the contract
	governanceAddr := k.GetAuthority() // Governance module address for WASM admin
	instantiateMsg := BridgeTokenInstantiateMsg{
		ChainId:         chainId,
		ContractAddress: contractAddress,
		InitialBalances: []Balance{},
		Mint: &MintInfo{
			Minter: k.AccountKeeper.GetModuleAddress(types.ModuleName).String(), // Inference module as minter
		},
		Admin: &governanceAddr, // Pass admin explicitly to avoid querying during instantiation
	}

	msgBz, err := json.Marshal(instantiateMsg)
	if err != nil {
		return "", err
	}

	// Instantiate the CW20 contract
	contractAddr, _, err := wasmKeeper.Instantiate(
		ctx,
		codeID,
		k.AccountKeeper.GetModuleAddress(types.ModuleName), // Instantiator: inference module
		sdk.MustAccAddressFromBech32(governanceAddr),       // Admin: governance module (for contract upgrades)
		msgBz,
		fmt.Sprintf("Bridged Token %s:%s", chainId, contractAddress),
		sdk.NewCoins(),
	)
	if err != nil {
		return "", err
	}

	k.LogInfo("Bridge exchange: Successfully created wrapped token contract",
		types.Messages,
		"chainId", chainId,
		"contractAddress", contractAddress,
		"wrappedContractAddress", contractAddr.String())

	wrappedContractAddr := strings.ToLower(contractAddr.String())
	k.SetWrappedTokenContract(ctx, types.BridgeWrappedTokenContract{
		ChainId:                chainId,
		ContractAddress:        contractAddress,
		WrappedContractAddress: wrappedContractAddr,
	})

	// Check if metadata exists and update the contract immediately after creation
	if metadata, metadataFound := k.GetTokenMetadata(ctx, chainId, contractAddress); metadataFound {
		err = k.updateWrappedTokenContractMetadata(ctx, wrappedContractAddr, metadata)
		if err != nil {
			k.LogError("Bridge exchange: Failed to update newly created wrapped token contract metadata", types.Messages, "error", err)
			// Don't fail the entire operation, just log the error
		} else {
			k.LogInfo("Bridge exchange: Successfully updated newly created wrapped token contract metadata",
				types.Messages,
				"chainId", chainId,
				"contractAddress", contractAddress,
				"wrappedContractAddress", wrappedContractAddr,
				"name", metadata.Name,
				"symbol", metadata.Symbol,
				"decimals", metadata.Decimals)
		}
	}

	return contractAddr.String(), nil
}

// updateWrappedTokenContractMetadata updates the metadata of an existing wrapped token contract
func (k Keeper) updateWrappedTokenContractMetadata(ctx sdk.Context, wrappedContractAddr string, metadata TokenMetadata) error {
	wasmKeeper := k.GetWasmKeeper()

	// Prepare update metadata message
	updateMetadataMsg := struct {
		UpdateMetadata struct {
			Name     string `json:"name"`
			Symbol   string `json:"symbol"`
			Decimals uint8  `json:"decimals"`
		} `json:"update_metadata"`
	}{
		UpdateMetadata: struct {
			Name     string `json:"name"`
			Symbol   string `json:"symbol"`
			Decimals uint8  `json:"decimals"`
		}{
			Name:     metadata.Name,
			Symbol:   metadata.Symbol,
			Decimals: metadata.Decimals,
		},
	}

	msgBz, err := json.Marshal(updateMetadataMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal update metadata message: %w", err)
	}

	// Execute update metadata message using PermissionedKeeper
	permissionedKeeper := wasmkeeper.NewDefaultPermissionKeeper(wasmKeeper)
	_, err = permissionedKeeper.Execute(
		ctx,
		sdk.MustAccAddressFromBech32(wrappedContractAddr),
		k.AccountKeeper.GetModuleAddress(types.ModuleName),
		msgBz,
		sdk.NewCoins(),
	)
	if err != nil {
		return fmt.Errorf("failed to execute update metadata: %w", err)
	}

	return nil
}

// MintTokens mints tokens to the specified address
func (k Keeper) MintTokens(ctx sdk.Context, contractAddr string, recipient string, amount string) error {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())

	// Validate that recipient is a valid cosmos address
	_, err := sdk.AccAddressFromBech32(recipient)
	if err != nil {
		return fmt.Errorf("invalid cosmos address: %v", err)
	}

	// Contract address should already be a cosmos address
	normalizedContractAddr := strings.ToLower(contractAddr)

	// Prepare mint message
	mintMsg := struct {
		Mint struct {
			Recipient string `json:"recipient"`
			Amount    string `json:"amount"`
		} `json:"mint"`
	}{
		Mint: struct {
			Recipient string `json:"recipient"`
			Amount    string `json:"amount"`
		}{
			Recipient: recipient,
			Amount:    amount,
		},
	}

	msgBz, err := json.Marshal(mintMsg)
	if err != nil {
		return err
	}

	// Execute mint message
	_, err = wasmKeeper.Execute(
		ctx,
		sdk.MustAccAddressFromBech32(normalizedContractAddr),
		k.AccountKeeper.GetModuleAddress(types.ModuleName),
		msgBz,
		sdk.NewCoins(),
	)
	return err
}

// handleCompletedBridgeTransaction handles minting tokens when a bridge transaction is completed
func (k Keeper) handleCompletedBridgeTransaction(ctx sdk.Context, bridgeTx *types.BridgeTransaction) error {
	// Check if this is a native token release (WGNK burn on Ethereum)
	isBridgeContract, chainId := k.IsBridgeContractAddress(ctx, bridgeTx.ContractAddress)
	if isBridgeContract {
		// Handle native token release from escrow
		err := k.HandleNativeTokenRelease(ctx, bridgeTx)
		if err != nil {
			k.LogError("Bridge exchange: Failed to release native tokens", types.Messages, "error", err)
			return fmt.Errorf("failed to release native tokens: %v", err)
		}

		k.LogInfo("Bridge exchange: Successfully released native tokens from escrow",
			types.Messages,
			"contractAddress", bridgeTx.ContractAddress,
			"recipient", bridgeTx.OwnerAddress,
			"amount", bridgeTx.Amount,
			"chainId", chainId)

		return nil
	}

	// Handle wrapped token minting (existing logic)
	// Get or create CW20 contract for the bridged token (automatically handles metadata)
	contractAddr, err := k.GetOrCreateWrappedTokenContract(ctx, bridgeTx.ChainId, bridgeTx.ContractAddress)
	if err != nil {
		k.LogError("Bridge exchange: Failed to get/create external token contract", types.Messages, "error", err)
		return fmt.Errorf("failed to handle token contract: %v", err)
	}

	// Mint tokens to the recipient
	err = k.MintTokens(ctx, contractAddr, bridgeTx.OwnerAddress, bridgeTx.Amount)
	if err != nil {
		k.LogError("Bridge exchange: Failed to mint external tokens", types.Messages, "error", err)
		return fmt.Errorf("failed to mint tokens: %v", err)
	}

	k.LogInfo("Bridge exchange: Successfully minted external tokens",
		types.Messages,
		"contract", contractAddr,
		"recipient", bridgeTx.OwnerAddress,
		"amount", bridgeTx.Amount)

	return nil
}

// GetAllBridgeTokenMetadata retrieves all bridge token metadata from chain state
func (k Keeper) GetAllBridgeTokenMetadata(ctx sdk.Context) []types.BridgeTokenMetadata {
	iter, err := k.WrappedTokenMetadataMap.Iterate(ctx, nil)
	if err != nil {
		k.LogError("Bridge exchange: Failed to iterate bridge token metadata", types.Messages, "error", err)
		return nil
	}
	defer iter.Close()

	metadataList, err := iter.Values()
	if err != nil {
		k.LogError("Bridge exchange: Failed to collect bridge token metadata", types.Messages, "error", err)
		return nil
	}

	return metadataList
}

// SetBridgeTradeApprovedToken stores a bridge trade approved token
func (k Keeper) SetBridgeTradeApprovedToken(ctx sdk.Context, approvedToken types.BridgeTokenReference) {
	// Validate input data before saving
	if err := k.validateBridgeTradeApprovedToken(&approvedToken); err != nil {
		k.LogError("Bridge exchange: Failed to save bridge trade approved token - validation failed",
			types.Messages,
			"chainId", approvedToken.ChainId,
			"contractAddress", approvedToken.ContractAddress,
			"error", err)
		panic(fmt.Sprintf("invalid bridge trade approved token data: %v", err))
	}

	normalizedContract := strings.ToLower(approvedToken.ContractAddress)
	approvedToken.ContractAddress = normalizedContract

	if err := k.LiquidityPoolApprovedTokensMap.Set(ctx, collections.Join(approvedToken.ChainId, normalizedContract), approvedToken); err != nil {
		panic(fmt.Sprintf("failed to store bridge trade approved token: %v", err))
	}

	k.LogInfo("Bridge trade approved token stored",
		types.Messages,
		"chainId", approvedToken.ChainId,
		"contractAddress", approvedToken.ContractAddress)
}

// validateBridgeTradeApprovedToken validates the approved token data before saving
func (k Keeper) validateBridgeTradeApprovedToken(approvedToken *types.BridgeTokenReference) error {
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

	// Validate contract address format
	if !isValidContractAddress(approvedToken.ContractAddress) {
		return fmt.Errorf("invalid contractAddress format: %s", approvedToken.ContractAddress)
	}

	// Check for reasonable length limits
	if len(approvedToken.ChainId) > 50 {
		return fmt.Errorf("chainId too long: %d characters", len(approvedToken.ChainId))
	}
	if len(approvedToken.ContractAddress) > 100 {
		return fmt.Errorf("contractAddress too long: %d characters", len(approvedToken.ContractAddress))
	}

	return nil
}

// HasBridgeTradeApprovedToken checks if a bridge trade approved token exists
func (k Keeper) HasBridgeTradeApprovedToken(ctx sdk.Context, chainId, contractAddress string) bool {
	has, err := k.LiquidityPoolApprovedTokensMap.Has(ctx, collections.Join(chainId, strings.ToLower(contractAddress)))
	if err != nil {
		return false
	}
	return has
}

// GetAllBridgeTradeApprovedTokens retrieves all bridge trade approved tokens
func (k Keeper) GetAllBridgeTradeApprovedTokens(ctx sdk.Context) []types.BridgeTokenReference {
	iter, err := k.LiquidityPoolApprovedTokensMap.Iterate(ctx, nil)
	if err != nil {
		k.LogError("Bridge exchange: Failed to iterate bridge trade approved tokens", types.Messages, "error", err)
		return nil
	}
	defer iter.Close()

	approvedTokens, err := iter.Values()
	if err != nil {
		k.LogError("Bridge exchange: Failed to collect bridge trade approved tokens", types.Messages, "error", err)
		return nil
	}

	return approvedTokens
}
