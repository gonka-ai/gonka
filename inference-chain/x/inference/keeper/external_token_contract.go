package keeper

import (
	"context"
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

// GetExternalTokenContract retrieves a token contract mapping
func (k Keeper) GetExternalTokenContract(ctx sdk.Context, externalChain, externalContract string) (types.ExternalTokenContract, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte(TokenContractKeyPrefix + externalChain + "/" + externalContract)

	bz := store.Get(key)
	if bz == nil {
		return types.ExternalTokenContract{}, false
	}

	var contract types.ExternalTokenContract
	k.cdc.MustUnmarshal(bz, &contract)
	return contract, true
}

// SetExternalTokenContract stores a token contract mapping
func (k Keeper) SetExternalTokenContract(ctx sdk.Context, contract types.ExternalTokenContract) {
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

func (k Keeper) GetContractsParams(ctx sdk.Context) (types.CosmWasmParams, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("contracts_params")
	bz := store.Get(key)
	if bz == nil {
		return types.CosmWasmParams{}, false
	}

	var contractsParams types.CosmWasmParams
	k.cdc.MustUnmarshal(bz, &contractsParams)
	return contractsParams, true
}

func (k Keeper) SetContractsParams(ctx context.Context, contractsParams types.CosmWasmParams) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("contracts_params")
	bz := k.cdc.MustMarshal(&contractsParams)
	store.Set(key, bz)
}

func (k Keeper) GetOrCreateExternalTokenContract(ctx sdk.Context, externalChain, externalContract string) (string, error) {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.getWasmKeeper())
	// Check if mapping already exists
	contract, found := k.GetExternalTokenContract(ctx, externalChain, externalContract)
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
	k.SetExternalTokenContract(ctx, types.ExternalTokenContract{
		ExternalChain:    externalChain,
		ExternalContract: externalContract,
		Cw20Contract:     strings.ToLower(contractAddr.String()),
		Name:             instantiateMsg.Name,
		Symbol:           instantiateMsg.Symbol,
		Decimals:         uint32(instantiateMsg.Decimals),
	})

	return contractAddr.String(), nil
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
	// Get or create CW20 contract for the bridged token
	contractAddr, err := k.GetOrCreateExternalTokenContract(ctx, bridgeTx.OriginChain, bridgeTx.ContractAddress)
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
