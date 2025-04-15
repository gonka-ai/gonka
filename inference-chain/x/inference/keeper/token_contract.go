package keeper

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// CW20InstantiateMsg represents the JSON message used to instantiate CW20 contract
type CW20InstantiateMsg struct {
	Name            string     `json:"name"`
	Symbol          string     `json:"symbol"`
	Decimals        uint8      `json:"decimals"`
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
	TokenContractKeyPrefix = "TokenContract/"
	TokenCodeIDKey         = "TokenCodeID"
)

// GetTokenContract retrieves a token contract mapping
func (k Keeper) GetTokenContract(ctx sdk.Context, externalChain, externalContract string) (types.TokenContract, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(TokenContractKeyPrefix + externalChain + "/" + externalContract)

	bz := store.Get(key)
	if bz == nil {
		return types.TokenContract{}, false
	}

	var contract types.TokenContract
	k.cdc.MustUnmarshal(bz, &contract)
	return contract, true
}

// SetTokenContract stores a token contract mapping
func (k Keeper) SetTokenContract(ctx sdk.Context, contract types.TokenContract) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(TokenContractKeyPrefix + contract.ExternalChain + "/" + contract.ExternalContract)

	bz := k.cdc.MustMarshal(&contract)
	store.Set(key, bz)
}

// GetTokenCodeID retrieves the stored CW20 code ID, uploading code if needed
func (k Keeper) GetTokenCodeID(ctx sdk.Context) (uint64, bool) {
	contractsParams, found := k.GetContractsParams(ctx)
	if !found {
		return 0, false
	}

	// Check if we have a code ID already
	if contractsParams.Cw20CodeId > 0 {
		return contractsParams.Cw20CodeId, true
	}

	// Check if we need to upload code
	if len(contractsParams.Cw20Code) > 0 {
		wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())

		// Upload the code
		codeID, _, err := wasmKeeper.Create(
			ctx,
			k.AccountKeeper.GetModuleAddress(types.ModuleName),
			contractsParams.Cw20Code,
			nil, // No instantiate permission
		)
		if err != nil {
			// Log error but don't panic
			return 0, false
		}

		// Update the code ID and clear code bytes to save state size
		contractsParams.Cw20CodeId = codeID
		contractsParams.Cw20Code = nil

		// Store the updated ContractsParams
		k.SetContractsParams(ctx, contractsParams)
		return codeID, true
	}

	return 0, false
}

func (k Keeper) GetContractsParams(ctx sdk.Context) (types.ContractsParams, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("contracts_params")
	bz := store.Get(key)
	if bz == nil {
		return types.ContractsParams{}, false
	}

	var contractsParams types.ContractsParams
	k.cdc.MustUnmarshal(bz, &contractsParams)
	return contractsParams, true
}

func (k Keeper) SetContractsParams(ctx sdk.Context, contractsParams types.ContractsParams) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("contracts_params")
	bz := k.cdc.MustMarshal(&contractsParams)
	store.Set(key, bz)
}

func (k Keeper) GetOrCreateTokenContract(ctx sdk.Context, externalChain, externalContract string) (string, error) {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.getWasmKeeper())
	// Check if mapping already exists
	contract, found := k.GetTokenContract(ctx, externalChain, externalContract)
	if found {
		return contract.Cw20Contract, nil
	}

	// Get the stored CW20 code ID
	codeID, found := k.GetTokenCodeID(ctx)
	if !found {
		return "", fmt.Errorf("CW20 code ID not found")
	}

	// Prepare instantiate message
	instantiateMsg := CW20InstantiateMsg{
		Name:            fmt.Sprintf("BT%s", externalContract[:8]), // "BT" for "Bridged Token" + first 8 chars of contract
		Symbol:          "bTKN",
		Decimals:        6,
		InitialBalances: []Balance{},
		Mint: &MintInfo{
			Minter: k.AccountKeeper.GetModuleAddress(types.ModuleName).String(),
		},
	}

	msgBz, err := json.Marshal(instantiateMsg)
	if err != nil {
		return "", err
	}

	// Instantiate the CW20 contract
	contractAddr, _, err := wasmKeeper.Instantiate(
		ctx,
		codeID,
		k.AccountKeeper.GetModuleAddress(types.ModuleName),
		k.AccountKeeper.GetModuleAddress(types.ModuleName),
		msgBz,
		fmt.Sprintf("Bridged Token %s", externalContract),
		sdk.NewCoins(),
	)
	if err != nil {
		return "", err
	}

	// Store the mapping
	k.SetTokenContract(ctx, types.TokenContract{
		ExternalChain:    externalChain,
		ExternalContract: externalContract,
		Cw20Contract:     strings.ToLower(contractAddr.String()),
		Name:             instantiateMsg.Name,
		Symbol:           instantiateMsg.Symbol,
		Decimals:         uint32(instantiateMsg.Decimals),
	})

	return contractAddr.String(), nil
}

// ConvertExternalAddressToCosmos converts external blockchain addresses (like Ethereum) to Cosmos bech32 addresses
func (k Keeper) ConvertExternalAddressToCosmos(ctx sdk.Context, address string) (string, error) {
	// If it's already a cosmos address, just return it
	_, err := sdk.AccAddressFromBech32(address)
	if err == nil {
		return address, nil
	}

	// Handle Ethereum addresses
	if strings.HasPrefix(address, "0x") {
		// For Ethereum addresses, create a deterministic mapping
		ethAddr := strings.ToLower(address)
		hasher := sha256.New()
		hasher.Write([]byte(ethAddr))
		hash := hasher.Sum(nil)

		// Create a cosmos address from the first 20 bytes of the hash
		cosmosAddr := sdk.AccAddress(hash[:20])

		k.LogInfo("Converted ETH address to Cosmos address",
			types.Messages,
			"eth_address", ethAddr,
			"cosmos_address", cosmosAddr.String())

		return cosmosAddr.String(), nil
	}

	// For other unknown formats
	return "", fmt.Errorf("unsupported address format: %s", address)
}

// MintTokens mints tokens to the specified address
func (k Keeper) MintTokens(ctx sdk.Context, contractAddr string, recipient string, amount string) error {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())

	// Convert recipient address if needed
	cosmosRecipient, err := k.ConvertExternalAddressToCosmos(ctx, recipient)
	if err != nil {
		return fmt.Errorf("invalid recipient address: %v", err)
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
			Recipient: cosmosRecipient,
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
	// Get or create CW20 contract for the bridged token
	contractAddr, err := k.GetOrCreateTokenContract(ctx, bridgeTx.OriginChain, bridgeTx.ContractAddress)
	if err != nil {
		k.LogError("Failed to get/create token contract", types.Messages, "error", err)
		return fmt.Errorf("failed to handle token contract: %v", err)
	}

	// Mint tokens to the recipient
	err = k.MintTokens(ctx, contractAddr, bridgeTx.OwnerAddress, bridgeTx.Amount)
	if err != nil {
		k.LogError("Failed to mint tokens", types.Messages, "error", err)
		return fmt.Errorf("failed to mint tokens: %v", err)
	}

	k.LogInfo("Successfully minted bridged tokens",
		types.Messages,
		"contract", contractAddr,
		"recipient", bridgeTx.OwnerAddress,
		"amount", bridgeTx.Amount)

	return nil
}
