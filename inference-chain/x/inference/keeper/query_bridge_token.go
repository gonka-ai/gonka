package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// verifyContractMinter verifies that a CW20 contract's minter is the inference module
func (k Keeper) verifyContractMinter(ctx sdk.Context, contractAddr string) bool {
	// Prepare the CW20 minter query message
	minterQuery := struct {
		Minter struct{} `json:"minter"`
	}{
		Minter: struct{}{},
	}

	queryBz, err := json.Marshal(minterQuery)
	if err != nil {
		return false
	}

	// Get the WASM keeper to query the contract
	wasmKeeper := k.GetWasmKeeper()

	// Parse the contract address
	contractAccAddr, err := sdk.AccAddressFromBech32(contractAddr)
	if err != nil {
		return false
	}

	// Query the WASM contract using QuerySmart
	response, err := wasmKeeper.QuerySmart(ctx, contractAccAddr, queryBz)
	if err != nil {
		return false
	}

	// Parse the response to extract the minter
	var minterResponse struct {
		Minter string `json:"minter"`
	}

	if err := json.Unmarshal(response, &minterResponse); err != nil {
		return false
	}

	// Check if the minter is the inference module
	expectedMinter := k.AccountKeeper.GetModuleAddress(types.ModuleName).String()
	return minterResponse.Minter == expectedMinter
}

// validateWrappedTokenForTradeInternal validates a wrapped token for trading through liquidity pools
// It performs three validations:
// 1. Verifies that the wrapped token contract was created through the chain (minter is module)
// 2. Verifies that the wrapped token has registered metadata
// 3. Verifies that the same metadata is registered in the list of approved for trade
func (k Keeper) validateWrappedTokenForTradeInternal(ctx context.Context, contractAddress string) (bool, *types.BridgeWrappedTokenContract, error) {
	// Normalize the contract address
	contractAddr := strings.ToLower(contractAddress)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Step 1: Find the wrapped token contract in our registry
	reference, err := k.WrappedContractReverseIndex.Get(sdkCtx, contractAddr)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false, nil, fmt.Errorf("wrapped token contract not found in registry")
		}
		k.LogError("Bridge exchange: Failed to lookup wrapped contract reference",
			types.Messages,
			"contract", contractAddr,
			"error", err)
		return false, nil, fmt.Errorf("failed to lookup wrapped token reference: %w", err)
	}

	wrapped, found := k.GetWrappedTokenContract(sdkCtx, reference.ChainId, reference.ContractAddress)
	if !found {
		return false, nil, fmt.Errorf("wrapped token contract not found in registry")
	}

	wrappedContract := &wrapped

	// Step 2: Verify that the wrapped token contract was created through the chain (minter is module)
	if !k.verifyContractMinter(sdkCtx, contractAddr) {
		return false, nil, fmt.Errorf("contract minter is not the inference module")
	}

	// Step 3: Verify that the wrapped token has registered metadata
	if _, metadataFound := k.GetTokenMetadata(sdkCtx, wrappedContract.ChainId, wrappedContract.ContractAddress); !metadataFound {
		return false, nil, fmt.Errorf("token metadata not found for chain %s, contract %s", wrappedContract.ChainId, wrappedContract.ContractAddress)
	}

	// Step 4: Verify that the same metadata is registered in the list of approved for trade
	if !k.HasBridgeTradeApprovedToken(sdkCtx, wrappedContract.ChainId, wrappedContract.ContractAddress) {
		return false, nil, fmt.Errorf("token is not approved for trading")
	}

	return true, wrappedContract, nil
}
