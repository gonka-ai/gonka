package keeper

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"cosmossdk.io/store/prefix"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/runtime"
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

const (
	TokenContractKeyPrefix          = "TokenContract/"
	WrappedTokenCodeIDKey           = "WrappedTokenCodeID"
	TokenMetadataKeyPrefix          = "TokenMetadata/"
	WrappedContractReverseKeyPrefix = "WrappedContractReverse/" // Index by wrapped contract address
)

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

	// Normalize contract address to lowercase for consistent storage
	normalizedContract := strings.ToLower(externalContract)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(TokenMetadataKeyPrefix + externalChain + "/" + normalizedContract)

	// Check if metadata already exists
	if existingMetadata, found := k.GetTokenMetadata(ctx, externalChain, externalContract); found {
		if !metadata.Overwrite {
			return fmt.Errorf("token metadata already exists for chain %s contract %s and overwrite is false", externalChain, externalContract)
		}

		k.LogInfo("Bridge exchange: Overwriting existing token metadata",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"oldName", existingMetadata.Name,
			"newName", metadata.Name,
			"oldSymbol", existingMetadata.Symbol,
			"newSymbol", metadata.Symbol,
			"oldDecimals", existingMetadata.Decimals,
			"newDecimals", metadata.Decimals)
	}

	// Create metadata without the Overwrite field for storage
	storageMetadata := TokenMetadata{
		Name:     metadata.Name,
		Symbol:   metadata.Symbol,
		Decimals: metadata.Decimals,
	}

	bz, err := json.Marshal(storageMetadata)
	if err != nil {
		k.LogError("Bridge exchange: Failed to marshal token metadata", types.Messages, "error", err)
		return fmt.Errorf("failed to marshal token metadata: %w", err)
	}

	store.Set(key, bz)

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
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	normalizedContract := strings.ToLower(externalContract)
	key := []byte(TokenMetadataKeyPrefix + externalChain + "/" + normalizedContract)

	bz := store.Get(key)
	if bz == nil {
		return TokenMetadata{}, false
	}

	var metadata TokenMetadata
	if err := json.Unmarshal(bz, &metadata); err != nil {
		k.LogError("Failed to unmarshal token metadata", types.Messages, "error", err)
		return TokenMetadata{}, false
	}

	return metadata, true
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

	// Normalize contract addresses to lowercase for consistent storage
	normalizedContract := strings.ToLower(contract.ContractAddress)
	normalizedWrappedContract := strings.ToLower(contract.WrappedContractAddress)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Store the main mapping: chainId/contractAddress -> BridgeWrappedTokenContract
	key := []byte(TokenContractKeyPrefix + contract.ChainId + "/" + normalizedContract)
	bz := k.cdc.MustMarshal(&contract)
	store.Set(key, bz)

	// Store the reverse index: wrappedContractAddress -> chainId/contractAddress
	k.setWrappedContractReverseIndex(ctx, normalizedWrappedContract, contract.ChainId, normalizedContract)

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

	// For Ethereum-style addresses, validate hex format
	if strings.HasPrefix(strings.ToLower(address), "0x") {
		hexPart := address[2:] // Remove 0x prefix
		if len(hexPart) != 40 {
			return false
		}
		// Check if it's valid hex
		for _, r := range hexPart {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
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
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Normalize contract address to lowercase for consistent retrieval
	normalizedContract := strings.ToLower(externalContract)
	key := []byte(TokenContractKeyPrefix + externalChain + "/" + normalizedContract)

	bz := store.Get(key)
	if bz == nil {
		return types.BridgeWrappedTokenContract{}, false
	}

	var contract types.BridgeWrappedTokenContract
	err := k.cdc.Unmarshal(bz, &contract)
	if err != nil {
		// Log the error and return false
		k.LogError("Bridge exchange: Failed to unmarshal wrapped token contract",
			types.Messages,
			"chain", externalChain,
			"contract", externalContract,
			"error", err)
		return types.BridgeWrappedTokenContract{}, false
	}
	return contract, true
}

// setWrappedContractReverseIndex stores the reverse index mapping
func (k Keeper) setWrappedContractReverseIndex(ctx sdk.Context, wrappedContractAddress, chainId, contractAddress string) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Create reverse index key: WrappedContractReverse/wrappedAddress
	reverseKey := []byte(WrappedContractReverseKeyPrefix + wrappedContractAddress)

	// Create the reverse index proto message
	reverseIndex := types.BridgeTokenReference{
		ChainId:         chainId,
		ContractAddress: contractAddress,
	}

	// Marshal and store the protobuf message
	bz := k.cdc.MustMarshal(&reverseIndex)
	store.Set(reverseKey, bz)
}

// GetWrappedTokenContractByWrappedAddress retrieves a wrapped token contract by its wrapped contract address
func (k Keeper) GetWrappedTokenContractByWrappedAddress(ctx sdk.Context, wrappedContractAddress string) (types.BridgeWrappedTokenContract, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Normalize wrapped contract address to lowercase
	normalizedWrappedContract := strings.ToLower(wrappedContractAddress)

	// Look up the reverse index
	reverseKey := []byte(WrappedContractReverseKeyPrefix + normalizedWrappedContract)
	bz := store.Get(reverseKey)
	if bz == nil {
		return types.BridgeWrappedTokenContract{}, false
	}

	// Unmarshal the reverse index protobuf message
	var reverseIndex types.BridgeTokenReference
	err := k.cdc.Unmarshal(bz, &reverseIndex)
	if err != nil {
		// Log error - corrupted index
		k.LogError("Bridge exchange: Failed to unmarshal reverse index entry",
			types.Messages,
			"wrappedContractAddress", wrappedContractAddress,
			"error", err)
		return types.BridgeWrappedTokenContract{}, false
	}

	// Now get the actual contract using the original lookup
	return k.GetWrappedTokenContract(ctx, reverseIndex.ChainId, reverseIndex.ContractAddress)
}

func (k Keeper) GetWrappedTokenCodeID(ctx sdk.Context) (uint64, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get([]byte(WrappedTokenCodeIDKey))
	if bz == nil || len(bz) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(bz), true
}

func (k Keeper) SetWrappedTokenCodeID(ctx sdk.Context, codeID uint64) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, codeID)
	store.Set([]byte(WrappedTokenCodeIDKey), buf)
}

// MigrateAllWrappedTokenContracts migrates all known wrapped token contract instances to the given code ID.
// The module account is the admin of these instances, so it can invoke Migrate.
// migrateMsg can be nil or an empty JSON object when no special migration data is needed.
func (k Keeper) MigrateAllWrappedTokenContracts(ctx sdk.Context, newCodeID uint64, migrateMsg json.RawMessage) error {
	// Iterate over all wrapped token mappings
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	pstore := prefix.NewStore(storeAdapter, []byte(TokenContractKeyPrefix))
	iterator := pstore.Iterator(nil, nil)
	defer iterator.Close()

	permissionedKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())
	adminAddr := k.AccountKeeper.GetModuleAddress(types.ModuleName)
	if len(migrateMsg) == 0 {
		migrateMsg = json.RawMessage([]byte("{}"))
	}

	var firstErr error
	for ; iterator.Valid(); iterator.Next() {
		var contract types.BridgeWrappedTokenContract
		if err := k.cdc.Unmarshal(iterator.Value(), &contract); err != nil {
			// Corrupted entry, record error and continue
			if firstErr == nil {
				firstErr = fmt.Errorf("unmarshal wrapped token mapping: %w", err)
			}
			continue
		}
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
	// Note: admin role will be auto-detected from WASM admin by the contract
	instantiateMsg := BridgeTokenInstantiateMsg{
		ChainId:         chainId,
		ContractAddress: contractAddress,
		InitialBalances: []Balance{},
		Mint: &MintInfo{
			Minter: k.AccountKeeper.GetModuleAddress(types.ModuleName).String(), // Inference module as minter
		},
	}

	msgBz, err := json.Marshal(instantiateMsg)
	if err != nil {
		return "", err
	}

	// Instantiate the CW20 contract
	governanceAddr := k.GetAuthority() // Governance module address for WASM admin
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
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	pstore := prefix.NewStore(storeAdapter, []byte(TokenMetadataKeyPrefix))
	iterator := pstore.Iterator(nil, nil)
	defer iterator.Close()

	var metadataList []types.BridgeTokenMetadata
	for ; iterator.Valid(); iterator.Next() {
		// Keys of the prefixed store are in the form: chainId/contractAddress
		chainContract := string(iterator.Key())

		// Split by "/" to get chain and contract
		parts := strings.Split(chainContract, "/")
		if len(parts) != 2 {
			continue
		}

		chainId := parts[0]
		contractAddress := parts[1]

		// Get the metadata
		if metadata, found := k.GetTokenMetadata(ctx, chainId, contractAddress); found {
			bridgeMetadata := types.BridgeTokenMetadata{
				ChainId:         chainId,
				ContractAddress: contractAddress,
				Name:            metadata.Name,
				Symbol:          metadata.Symbol,
				Decimals:        uint32(metadata.Decimals),
			}
			metadataList = append(metadataList, bridgeMetadata)
		}
	}

	return metadataList
}

// Bridge trade approved token storage keys
const (
	BridgeTradeApprovedTokenKeyPrefix = "BridgeTradeApprovedToken/"
)

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

	// Normalize contract address to lowercase for consistent storage
	normalizedContract := strings.ToLower(approvedToken.ContractAddress)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(BridgeTradeApprovedTokenKeyPrefix + approvedToken.ChainId + "/" + normalizedContract)

	bz := k.cdc.MustMarshal(&approvedToken)
	store.Set(key, bz)

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
	// Normalize contract address to lowercase for consistent retrieval
	normalizedContract := strings.ToLower(contractAddress)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(BridgeTradeApprovedTokenKeyPrefix + chainId + "/" + normalizedContract)

	bz := store.Get(key)
	return bz != nil
}

// GetAllBridgeTradeApprovedTokens retrieves all bridge trade approved tokens
func (k Keeper) GetAllBridgeTradeApprovedTokens(ctx sdk.Context) []types.BridgeTokenReference {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	pstore := prefix.NewStore(storeAdapter, []byte(BridgeTradeApprovedTokenKeyPrefix))
	iterator := pstore.Iterator(nil, nil)
	defer iterator.Close()

	var approvedTokens []types.BridgeTokenReference
	for ; iterator.Valid(); iterator.Next() {
		var approvedToken types.BridgeTokenReference
		err := k.cdc.Unmarshal(iterator.Value(), &approvedToken)
		if err != nil {
			// Log the error but continue processing other tokens
			k.LogError("Bridge exchange: Failed to unmarshal bridge trade approved token",
				types.Messages,
				"key", string(iterator.Key()),
				"error", err)
			continue
		}

		// Validate and skip invalid entries to avoid returning corrupted data
		if err := k.validateBridgeTradeApprovedToken(&approvedToken); err != nil {
			k.LogError("Bridge exchange: Skipping invalid bridge trade approved token",
				types.Messages,
				"key", string(iterator.Key()),
				"error", err)
			continue
		}

		approvedTokens = append(approvedTokens, approvedToken)
	}

	return approvedTokens
}
