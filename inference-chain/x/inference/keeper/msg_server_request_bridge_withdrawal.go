package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// 1. Get chain ID for request identification
	chainID := ctx.ChainID()

	// 2. Generate request ID from transaction hash
	requestID := k.generateRequestID(ctx)

	// 3. Get current epoch for BLS signature
	currentEpochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current epoch group: %v", err)
	}

	// 4. Get wrapped token metadata to determine destination chain
	metadata, found := k.getWrappedTokenMetadata(ctx, msg.WrappedContractAddress)
	if !found {
		return nil, fmt.Errorf("wrapped token contract not found: %s", msg.WrappedContractAddress)
	}

	// 5. Execute CW20 withdrawal/burn via the contract
	err = k.executeCW20Withdrawal(ctx, msg.Creator, msg.WrappedContractAddress, msg.Amount)
	if err != nil {
		return nil, fmt.Errorf("failed to withdraw CW20 tokens: %v", err)
	}

	// 6. Prepare BLS signature data according to Ethereum bridge format
	// Get numeric chain ID from metadata's string chain identifier
	destinationChainId, found := chainIdMapping[metadata.ChainId]
	if !found {
		return nil, fmt.Errorf("unsupported destination chain: %s", metadata.ChainId)
	}

	blsData := k.prepareBridgeSignatureData(
		destinationChainId, // Numeric chain ID (e.g., "1", "137")
		msg.DestinationAddress,
		metadata.OriginContractAddress, // Original token address on destination chain
		msg.Amount,                     // amount as string
	)

	// 7. Request BLS threshold signature
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

	// 8. Log the withdrawal request
	k.LogInfo("Bridge withdrawal requested", types.Messages,
		"creator", msg.Creator,
		"wrapped_contract", msg.WrappedContractAddress,
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

// generateRequestID creates a unique request ID from transaction hash
func (k msgServer) generateRequestID(ctx sdk.Context) string {
	// Get transaction hash from context
	txHash := ctx.TxBytes()
	if len(txHash) == 0 {
		// Fallback: use block height + block hash for uniqueness
		blockInfo := fmt.Sprintf("%d-%s", ctx.BlockHeight(), hex.EncodeToString(ctx.BlockHeader().LastBlockId.Hash))
		hash := sha256.Sum256([]byte(blockInfo))
		return hex.EncodeToString(hash[:])
	}

	// Use transaction hash directly
	hash := sha256.Sum256(txHash)
	return hex.EncodeToString(hash[:])
}

// WrappedTokenMetadata holds metadata for wrapped token contracts
type WrappedTokenMetadata struct {
	ChainId               string
	OriginContractAddress string
	Name                  string
	Symbol                string
	Decimals              uint8
}

// getWrappedTokenMetadata retrieves metadata for wrapped token contract
func (k msgServer) getWrappedTokenMetadata(ctx sdk.Context, contractAddress string) (*WrappedTokenMetadata, bool) {
	// Use the efficient reverse index lookup to find the wrapped contract
	wrappedContract, found := k.GetWrappedTokenContractByWrappedAddress(ctx, contractAddress)
	if !found {
		return nil, false
	}

	// Get the token metadata for the original contract
	metadata, found := k.GetTokenMetadata(ctx, wrappedContract.ChainId, wrappedContract.ContractAddress)
	if !found {
		return nil, false
	}

	return &WrappedTokenMetadata{
		ChainId:               wrappedContract.ChainId,
		OriginContractAddress: wrappedContract.ContractAddress,
		Name:                  metadata.Name,
		Symbol:                metadata.Symbol,
		Decimals:              metadata.Decimals,
	}, true
}

// executeCW20Withdrawal burns/withdraws tokens from the user's wrapped token balance
func (k msgServer) executeCW20Withdrawal(ctx sdk.Context, userAddress, contractAddress, amount string) error {
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.getWasmKeeper())

	// Create withdrawal message for the CW20 contract
	withdrawMsg := fmt.Sprintf(`{"withdraw":{"amount":"%s"}}`, amount)

	// Convert user address to AccAddress for proper authentication
	userAddr, err := sdk.AccAddressFromBech32(userAddress)
	if err != nil {
		return fmt.Errorf("invalid user address: %v", err)
	}

	// Execute the withdrawal message
	contractAddr, err := sdk.AccAddressFromBech32(contractAddress)
	if err != nil {
		return fmt.Errorf("invalid contract address: %v", err)
	}

	// Execute as the USER (not as module admin) to preserve user authority
	// This ensures the contract sees info.sender = user and can properly authenticate
	_, err = wasmKeeper.Execute(
		ctx,
		contractAddr,        // Contract to execute
		userAddr,            // USER as the message sender/authority
		[]byte(withdrawMsg), // Withdraw message
		sdk.NewCoins(),      // No coins attached to this message
	)
	if err != nil {
		return fmt.Errorf("contract execution failed: %v", err)
	}

	return nil
}

// prepareBridgeSignatureData formats data according to Ethereum bridge contract expectations
func (k msgServer) prepareBridgeSignatureData(destinationChainId, recipient, tokenContract, amount string) [][]byte {
	// Convert data to the format expected by BLS signing and Ethereum bridge
	// Note: epochId, gonka-chainId, and requestId are provided separately in SigningData structure
	// Final message structure: [epochId, gonka-chainId, requestId, destinationChainId, WITHDRAW_OPERATION, recipient, tokenContract, amount]
	// This provides cross-chain replay protection for both Gonka and destination chains

	// Pad destination chain ID to 32 bytes for consistency
	destinationChainBytes := make([]byte, 32)
	copy(destinationChainBytes[32-len(destinationChainId):], destinationChainId)

	data := [][]byte{
		destinationChainBytes,    // destination chain ID (e.g., "1" for Ethereum mainnet)
		withdrawOperationHash[:], // operation type hash (pre-calculated constant)
		[]byte(recipient),        // recipient address
		[]byte(tokenContract),    // token contract address
		[]byte(amount),           // amount as bytes
	}

	return data
}
