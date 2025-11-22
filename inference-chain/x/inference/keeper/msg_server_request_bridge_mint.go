package keeper

import (
	"context"
	"crypto/sha256"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

// Bridge operation constants for native token minting (matches BridgeContract.sol)
var (
	// MINT_OPERATION hash - calculated once at package initialization using keccak256
	mintOperationHash = keccak256Hash([]byte("MINT_OPERATION"))

	// Chain ID mapping for mint operations (same as withdrawal)
	mintChainIdMapping = map[string]string{
		"ethereum": "1",        // Ethereum mainnet
		"sepolia":  "11155111", // Ethereum Sepolia testnet
		"polygon":  "137",      // Polygon mainnet
		"mumbai":   "80001",    // Polygon Mumbai testnet
		"arbitrum": "42161",    // Arbitrum One
	}
)

func (k msgServer) RequestBridgeMint(goCtx context.Context, msg *types.MsgRequestBridgeMint) (*types.MsgRequestBridgeMintResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// 1. Get the user address
	userAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("invalid creator address: %v", err)
	}

	k.LogInfo("Bridge mint: Processing native token bridge request",
		types.Messages,
		"user", msg.Creator,
		"amount", msg.Amount,
		"destinationAddress", msg.DestinationAddress,
		"chainId", msg.ChainId)

	// 2. Validate the bridge mint request
	err = k.ValidateBridgeMintRequest(ctx, userAddr, msg.Amount, msg.ChainId)
	if err != nil {
		k.LogError("Bridge mint: Validation failed", types.Messages, "error", err)
		return nil, fmt.Errorf("bridge mint validation failed: %v", err)
	}

	// 3. Parse amount and create coins
	amountInt, ok := math.NewIntFromString(msg.Amount)
	if !ok {
		return nil, fmt.Errorf("invalid amount format: %s", msg.Amount)
	}
	nativeCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, amountInt))

	// 4. Transfer native tokens to bridge escrow account (atomic operation)
	err = k.TransferToEscrow(ctx, userAddr, nativeCoins)
	if err != nil {
		k.LogError("Bridge mint: Failed to transfer tokens to escrow", types.Messages, "error", err)
		return nil, fmt.Errorf("failed to transfer tokens to escrow: %v", err)
	}

	// 5. Generate request ID from transaction context
	requestID := k.generateRequestID(ctx)

	// 6. Get current epoch for BLS signature
	currentEpochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		// Rollback the escrow transfer if epoch retrieval fails
		rollbackErr := k.ReleaseFromEscrow(ctx, userAddr, nativeCoins)
		if rollbackErr != nil {
			k.LogError("Bridge mint: Failed to rollback escrow transfer", types.Messages, "rollbackError", rollbackErr)
		}
		return nil, fmt.Errorf("failed to get current epoch group: %v", err)
	}

	// 7. Prepare BLS signature data for WGNK minting on Ethereum
	// Get numeric chain ID from string chain identifier
	destinationChainId, found := mintChainIdMapping[msg.ChainId]
	if !found {
		// Rollback the escrow transfer
		rollbackErr := k.ReleaseFromEscrow(ctx, userAddr, nativeCoins)
		if rollbackErr != nil {
			k.LogError("Bridge mint: Failed to rollback escrow transfer", types.Messages, "rollbackError", rollbackErr)
		}
		return nil, fmt.Errorf("unsupported destination chain: %s", msg.ChainId)
	}

	// Prepare BLS signature data for WGNK mint command
	// Only prepare the data portion - BLS system will prepend epochId, gonkaChainId, requestId
	blsData := k.prepareBridgeMintSignatureData(
		destinationChainId,     // Numeric chain ID (e.g., "1", "137")
		msg.DestinationAddress, // Ethereum address to receive WGNK
		msg.Amount,             // Amount as string
	)

	// 8. Request BLS threshold signature for WGNK minting
	// Use the actual Gonka chain ID from context (source chain)
	gonkaChainID := ctx.ChainID()
	gonkaChainIdHash := sha256.Sum256([]byte(gonkaChainID)) // Convert to bytes32
	requestIdHash := keccak256Hash([]byte(requestID))

	signingData := blstypes.SigningData{
		CurrentEpochId: currentEpochGroup.GroupData.EpochIndex,
		ChainId:        gonkaChainIdHash[:], // GONKA_CHAIN_ID (32 bytes) - SOURCE chain
		RequestId:      requestIdHash[:],    // Request ID as bytes32 (32 bytes)
		Data:           blsData,             // The remaining data fields
	}

	err = k.BlsKeeper.RequestThresholdSignature(ctx, signingData)
	if err != nil {
		// Rollback the escrow transfer if BLS request fails
		rollbackErr := k.ReleaseFromEscrow(ctx, userAddr, nativeCoins)
		if rollbackErr != nil {
			k.LogError("Bridge mint: Failed to rollback escrow transfer", types.Messages, "rollbackError", rollbackErr)
		}
		k.LogError("Bridge mint: Failed to request BLS signature", types.Messages, "error", err)
		return nil, fmt.Errorf("failed to request BLS signature: %v", err)
	}

	// Generate BLS request ID for tracking (use request ID for simplicity)
	blsRequestId := requestID

	k.LogInfo("Bridge mint: Successfully processed native token bridge request",
		types.Messages,
		"user", msg.Creator,
		"amount", msg.Amount,
		"destinationAddress", msg.DestinationAddress,
		"chainId", msg.ChainId,
		"requestId", requestID,
		"epochIndex", currentEpochGroup.GroupData.EpochIndex,
		"blsRequestId", blsRequestId)

	// 9. Emit bridge mint event for off-chain monitoring
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"bridge_mint_requested",
			sdk.NewAttribute("user", msg.Creator),
			sdk.NewAttribute("amount", msg.Amount),
			sdk.NewAttribute("destination_address", msg.DestinationAddress),
			sdk.NewAttribute("chain_id", msg.ChainId),
			sdk.NewAttribute("request_id", requestID),
			sdk.NewAttribute("epoch_index", fmt.Sprintf("%d", currentEpochGroup.GroupData.EpochIndex)),
			sdk.NewAttribute("bls_request_id", blsRequestId),
		),
	)

	return &types.MsgRequestBridgeMintResponse{
		RequestId:    requestID,
		EpochIndex:   currentEpochGroup.GroupData.EpochIndex,
		BlsRequestId: blsRequestId,
	}, nil
}

// prepareBridgeMintSignatureData prepares the data for BLS signature according to Ethereum bridge format
func (k Keeper) prepareBridgeMintSignatureData(chainId, recipient, amount string) [][]byte {
	// This function only prepares the data that comes AFTER epochId, gonkaChainId, and requestId
	// Final message format: [epochId, gonkaChainId, requestId, ethereumChainId, MINT_OPERATION, recipient, amount]
	// BLS system will prepend: epochId (8 bytes) + gonkaChainId (32 bytes) + requestId (32 bytes)

	// Use helper functions for consistent encoding
	ethereumChainIdBytes := chainIdToBytes32(chainId)
	recipientBytes := ethereumAddressToBytes(recipient)
	amountBytes := amountToBytes32(amount)

	// Return the data fields that come after epochId, gonkaChainId, requestId
	// Order: ethereumChainId (32 bytes) + MINT_OPERATION (32 bytes) + recipient (20 bytes) + amount (32 bytes)
	data := [][]byte{
		ethereumChainIdBytes, // ETHEREUM_CHAIN_ID (32 bytes)
		mintOperationHash[:], // MINT_OPERATION hash (32 bytes)
		recipientBytes,       // Recipient address (20 bytes)
		amountBytes,          // Amount as uint256 (32 bytes)
	}

	return data
}
