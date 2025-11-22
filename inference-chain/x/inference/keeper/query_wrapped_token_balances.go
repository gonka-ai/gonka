package keeper

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WrappedTokenBalances returns all wrapped token balances for a specific cosmos address
func (k Keeper) WrappedTokenBalances(goCtx context.Context, req *types.QueryWrappedTokenBalancesRequest) (*types.QueryWrappedTokenBalancesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	address := strings.TrimSpace(req.Address)

	if address == "" {
		return nil, status.Error(codes.InvalidArgument, "address cannot be empty")
	}

	// Validate the cosmos address format
	if _, err := sdk.AccAddressFromBech32(address); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("invalid cosmos address: %v", err))
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Get all external token contracts (legitimate bridge tokens)
	externalTokens := k.getAllWrappedTokenContracts(ctx)

	var balances []*types.WrappedTokenBalance

	for i := range externalTokens {
		tokenContract := externalTokens[i]
		// Query the CW20 balance for this address
		balance, err := k.queryTokenBalance(ctx, tokenContract.WrappedContractAddress, address)
		if err != nil {
			k.LogWarn("Failed to query token balance",
				types.Messages,
				"contract", tokenContract.WrappedContractAddress,
				"address", address,
				"error", err)
			continue // Skip this token if balance query fails
		}

		// Get token metadata to retrieve decimals and symbol
		metadata, found := k.GetTokenMetadata(ctx, tokenContract.ChainId, tokenContract.ContractAddress)
		decimals := uint32(0) // Default to 0 if metadata not found
		symbol := ""          // Default to empty string if metadata not found
		if found {
			decimals = uint32(metadata.Decimals)
			symbol = metadata.Symbol
		}

		// Format the balance for human readability
		formattedBalance := k.formatTokenBalance(balance, decimals)

		wrappedBalance := &types.WrappedTokenBalance{
			TokenInfo:        &externalTokens[i],
			Symbol:           symbol,
			Balance:          balance,
			Decimals:         fmt.Sprintf("%d", decimals),
			FormattedBalance: formattedBalance,
		}

		balances = append(balances, wrappedBalance)
	}

	return &types.QueryWrappedTokenBalancesResponse{
		Balances: balances,
	}, nil
}

// getAllExternalTokenContracts returns all external token contracts from chain state
func (k Keeper) getAllWrappedTokenContracts(ctx sdk.Context) []types.BridgeWrappedTokenContract {
	iter, err := k.WrappedTokenContractsMap.Iterate(ctx, nil)
	if err != nil {
		k.LogWarn("Failed to iterate wrapped token contracts",
			types.Messages,
			"error", err)
		return nil
	}
	defer iter.Close()

	contracts, err := iter.Values()
	if err != nil {
		k.LogWarn("Failed to collect wrapped token contracts",
			types.Messages,
			"error", err)
		return nil
	}

	filtered := make([]types.BridgeWrappedTokenContract, 0, len(contracts))
	for _, contract := range contracts {
		if !k.isValidBridgeWrappedTokenContract(&contract) {
			k.LogWarn("Skipping corrupted wrapped token contract",
				types.Messages,
				"chainId", contract.ChainId,
				"contractAddress", contract.ContractAddress,
				"wrappedContractAddress", contract.WrappedContractAddress)
			continue
		}

		k.LogDebug("Found wrapped token contract",
			types.Messages,
			"chainId", contract.ChainId,
			"contractAddress", contract.ContractAddress,
			"wrappedContractAddress", contract.WrappedContractAddress)
		filtered = append(filtered, contract)
	}

	k.LogDebug("Total wrapped token contracts found",
		types.Messages,
		"count", len(filtered))

	return filtered
}

// isValidBridgeWrappedTokenContract validates that a BridgeWrappedTokenContract is not corrupted
func (k Keeper) isValidBridgeWrappedTokenContract(contract *types.BridgeWrappedTokenContract) bool {
	// Check if any of the fields contain binary data or invalid characters
	if contract == nil {
		return false
	}

	// Check for common corruption indicators (binary data, control characters, etc.)
	if containsBinaryData(contract.ChainId) || containsBinaryData(contract.ContractAddress) || containsBinaryData(contract.WrappedContractAddress) {
		return false
	}

	// Check if fields are empty or contain only whitespace
	if strings.TrimSpace(contract.ChainId) == "" || strings.TrimSpace(contract.ContractAddress) == "" || strings.TrimSpace(contract.WrappedContractAddress) == "" {
		return false
	}

	return true
}

// containsBinaryData checks if a string contains binary data or control characters
func containsBinaryData(s string) bool {
	for _, r := range s {
		// Check for control characters (0-31) except for common whitespace (9, 10, 13)
		if r < 32 && r != 9 && r != 10 && r != 13 {
			return true
		}
		// Check for replacement character () which indicates UTF-8 decoding issues
		if r == 0xFFFD {
			return true
		}
	}
	return false
}

// queryTokenBalance queries the CW20 contract to get the balance for a specific address
func (k Keeper) queryTokenBalance(ctx sdk.Context, contractAddr, address string) (string, error) {
	// Prepare the CW20 balance query message
	balanceQuery := struct {
		Balance struct {
			Address string `json:"address"`
		} `json:"balance"`
	}{
		Balance: struct {
			Address string `json:"address"`
		}{
			Address: address,
		},
	}

	queryBz, err := json.Marshal(balanceQuery)
	if err != nil {
		return "0", fmt.Errorf("failed to marshal balance query: %v", err)
	}

	// Get the WASM keeper to query the contract
	wasmKeeper := k.GetWasmKeeper()

	// Parse the contract address
	contractAccAddr, err := sdk.AccAddressFromBech32(contractAddr)
	if err != nil {
		k.LogWarn("Invalid contract address format",
			types.Messages,
			"contract", contractAddr,
			"address", address,
			"error", err)
		return "0", nil
	}

	// Query the WASM contract using QuerySmart
	response, err := wasmKeeper.QuerySmart(ctx, contractAccAddr, queryBz)
	if err != nil {
		// Log the error for debugging but return "0" to avoid breaking the entire query
		k.LogWarn("Failed to query WASM contract balance",
			types.Messages,
			"contract", contractAddr,
			"address", address,
			"error", err)
		return "0", nil // Return nil error to continue processing other tokens
	}

	// Check if response is empty
	if len(response) == 0 {
		k.LogWarn("Empty response from WASM contract",
			types.Messages,
			"contract", contractAddr,
			"address", address)
		return "0", nil
	}

	// Log the raw response for debugging
	k.LogDebug("Raw response from WASM contract",
		types.Messages,
		"contract", contractAddr,
		"address", address,
		"response", string(response))

	// Parse the response to extract the balance
	var balanceResponse struct {
		Balance string `json:"balance"`
	}

	if err := json.Unmarshal(response, &balanceResponse); err != nil {
		// Log the error for debugging but return "0" to avoid breaking the entire query
		k.LogWarn("Failed to unmarshal balance response",
			types.Messages,
			"contract", contractAddr,
			"address", address,
			"response", string(response),
			"error", err)
		return "0", nil // Return nil error to continue processing other tokens
	}

	// Log the parsed balance for debugging
	k.LogDebug("Successfully parsed balance response",
		types.Messages,
		"contract", contractAddr,
		"address", address,
		"balance", balanceResponse.Balance)

	return balanceResponse.Balance, nil
}

// formatTokenBalance formats the raw balance string into a human-readable format
func (k Keeper) formatTokenBalance(balance string, decimals uint32) string {
	// Parse the balance as a big integer
	balanceBig, success := new(big.Int).SetString(balance, 10)
	if !success {
		return "0"
	}

	// If balance is 0, return "0"
	if balanceBig.Cmp(big.NewInt(0)) == 0 {
		return "0"
	}

	// Calculate the divisor (10^decimals)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)

	// Get the integer and fractional parts
	integerPart := new(big.Int).Div(balanceBig, divisor)
	remainder := new(big.Int).Mod(balanceBig, divisor)

	// If there's no fractional part, return just the integer
	if remainder.Cmp(big.NewInt(0)) == 0 {
		return integerPart.String()
	}

	// Format the fractional part with proper leading zeros
	fractionalStr := remainder.String()

	// Pad with leading zeros if necessary
	for len(fractionalStr) < int(decimals) {
		fractionalStr = "0" + fractionalStr
	}

	// Remove trailing zeros from fractional part
	fractionalStr = strings.TrimRight(fractionalStr, "0")

	// If all fractional digits were zeros, return just integer
	if fractionalStr == "" {
		return integerPart.String()
	}

	return fmt.Sprintf("%s.%s", integerPart.String(), fractionalStr)
}
