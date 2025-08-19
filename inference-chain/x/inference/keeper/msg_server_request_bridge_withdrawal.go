package keeper

import (
	"context"
	"crypto/sha256"
	"fmt"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

// Bridge operation constants (matches BridgeContract.sol)
var (
	// WITHDRAW_OPERATION hash - calculated once at package initialization
	withdrawOperationHash = sha256.Sum256([]byte("WITHDRAW_OPERATION"))

	// Chain ID mapping: string identifier → numeric chain ID
	chainIdMapping = map[string]string{
		"ethereum": "1",        // Ethereum mainnet
		"sepolia":  "11155111", // Ethereum Sepolia testnet
		"polygon":  "137",      // Polygon mainnet
		"mumbai":   "80001",    // Polygon Mumbai testnet
		"arbitrum": "42161",    // Arbitrum One
		"optimism": "10",       // Optimism mainnet
	}
)

func (k msgServer) RequestBridgeWithdrawal(goCtx context.Context, msg *types.MsgRequestBridgeWithdrawal) (*types.MsgRequestBridgeWithdrawalResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// 1. Get the actual transaction signer (this is validated by the Cosmos SDK framework)
	signers := msg.GetSigners()
	if len(signers) != 1 {
		return nil, fmt.Errorf("expected exactly one signer, got %d", len(signers))
	}
	contractAddr := signers[0]
	contractAddrStr := contractAddr.String()

	// 2. Verify that the caller is actually a smart contract
	err := k.validateContractCaller(ctx, contractAddrStr)
	if err != nil {
		return nil, fmt.Errorf("caller validation failed: %v", err)
	}

	// 3. Verify that the calling contract is a registered wrapped token contract
	bridgeWrappedTokenContract, found := k.getWrappedTokenMetadata(ctx, contractAddrStr)
	if !found {
		return nil, fmt.Errorf("calling contract %s is not a registered wrapped token contract", contractAddrStr)
	}

	// 4. Get chain ID for request identification
	chainID := ctx.ChainID()

	// 5. Generate request ID from transaction hash
	requestID := k.generateRequestID(ctx)

	// 6. Get current epoch for BLS signature
	currentEpochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current epoch group: %v", err)
	}

	// 7. Prepare BLS signature data according to Ethereum bridge format
	// Get numeric chain ID from metadata's string chain identifier
	destinationChainId, found := chainIdMapping[bridgeWrappedTokenContract.ChainId]
	if !found {
		return nil, fmt.Errorf("unsupported destination chain: %s", bridgeWrappedTokenContract.ChainId)
	}

	blsData := k.prepareBridgeSignatureData(
		destinationChainId, // Numeric chain ID (e.g., "1", "137")
		msg.DestinationAddress,
		bridgeWrappedTokenContract.ContractAddress, // Original token address on destination chain
		msg.Amount, // amount as string
	)

	// 8. Request BLS threshold signature
	signingData := blstypes.SigningData{
		CurrentEpochId: currentEpochGroup.GroupData.EpochId,
		ChainId:        []byte(chainID),
		RequestId:      []byte(requestID),
		Data:           blsData,
	}

	err = k.BlsKeeper.RequestThresholdSignature(ctx, signingData)
	if err != nil {
		return nil, fmt.Errorf("failed to request BLS signature: %v", err)
	}

	// 9. Log the withdrawal request
	k.LogInfo("Contract bridge withdrawal requested", types.Messages,
		"contract_address", contractAddrStr,
		"user_address", msg.UserAddress,
		"amount", msg.Amount,
		"destination_address", msg.DestinationAddress,
		"request_id", requestID,
		"epoch_id", currentEpochGroup.GroupData.EpochId,
		"chain_id", chainID,
	)

	return &types.MsgRequestBridgeWithdrawalResponse{
		RequestId:    requestID,
		EpochId:      currentEpochGroup.GroupData.EpochId,
		BlsRequestId: requestID, // Use same ID for simplicity
	}, nil
}

// validateContractCaller ensures that the caller is actually a smart contract
func (k msgServer) validateContractCaller(ctx sdk.Context, contractAddress string) error {
	contractAddr, err := sdk.AccAddressFromBech32(contractAddress)
	if err != nil {
		return fmt.Errorf("invalid contract address: %v", err)
	}

	// Check if the address is a contract by querying contract info
	wasmKeeper := k.getWasmKeeper()
	contractInfo := wasmKeeper.GetContractInfo(ctx, contractAddr)
	if contractInfo == nil {
		return fmt.Errorf("address %s is not a smart contract", contractAddress)
	}

	return nil
}

// Helper function to get wasm keeper
func (k msgServer) getWasmKeeper() wasmkeeper.Keeper {
	if k.Keeper.getWasmKeeper == nil {
		panic("wasm keeper not available")
	}
	return k.Keeper.getWasmKeeper()
}

// Helper function to get wrapped token metadata using the keeper's existing method
func (k msgServer) getWrappedTokenMetadata(ctx sdk.Context, contractAddress string) (*types.BridgeWrappedTokenContract, bool) {
	contract, found := k.Keeper.GetWrappedTokenContractByWrappedAddress(ctx, contractAddress)
	if !found {
		return nil, false
	}
	return &contract, true
}

// Generate a unique request ID based on the transaction context
func (k msgServer) generateRequestID(ctx sdk.Context) string {
	// Use block height and transaction hash as a simple request ID
	// In a real implementation, you might want to use the transaction hash
	return fmt.Sprintf("req_%d_%x", ctx.BlockHeight(), ctx.TxBytes())
}

// Prepare BLS signature data for bridge withdrawal (matches Ethereum bridge contract format)
func (k msgServer) prepareBridgeSignatureData(chainId, destinationAddress, tokenAddress, amount string) [][]byte {
	// Bridge signature data format should match the Ethereum bridge contract expectations
	// This typically includes: operation hash, chain ID, destination address, token address, amount

	// Convert operation hash to bytes
	operationBytes := withdrawOperationHash[:]

	// Prepare data components (as bytes)
	data := [][]byte{
		operationBytes,             // WITHDRAW_OPERATION hash
		[]byte(chainId),            // Chain ID
		[]byte(destinationAddress), // Destination address
		[]byte(tokenAddress),       // Token contract address
		[]byte(amount),             // Amount as string
	}

	return data
}
