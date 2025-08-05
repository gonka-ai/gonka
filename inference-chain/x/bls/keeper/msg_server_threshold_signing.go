package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
	"golang.org/x/crypto/sha3"
)

// Note: We do this for security, so that none can mimic module originated signatures
// Domain separation constant for external threshold signature requests
var CUSTOM_SIGNATURE_DOMAIN = func() []byte {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte("CUSTOM_THRESHOLD_SIGNATURE"))
	result := hash.Sum(nil)
	return result[:32] // Return 32 bytes for bytes32 compatibility
}()

// SubmitPartialSignature handles the submission of partial signatures for threshold signing
func (ms msgServer) SubmitPartialSignature(ctx context.Context, msg *types.MsgSubmitPartialSignature) (*types.MsgSubmitPartialSignatureResponse, error) {
	// Convert to SDK context
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Call the core AddPartialSignature function which handles:
	// 1. Validates the request exists and is in COLLECTING_SIGNATURES status
	// 2. Verifies participant owns the claimed slot indices
	// 3. Verifies the partial signature cryptographically using shared BLS functions
	// 4. Aggregates signatures and checks threshold
	// 5. Emits completion/failure events as needed
	err := ms.AddPartialSignature(sdkCtx, msg.RequestId, msg.SlotIndices, msg.PartialSignature, msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("failed to add partial signature: %w", err)
	}

	return &types.MsgSubmitPartialSignatureResponse{}, nil
}

// RequestThresholdSignature handles requests for threshold signatures from external users
func (ms msgServer) RequestThresholdSignature(ctx context.Context, msg *types.MsgRequestThresholdSignature) (*types.MsgRequestThresholdSignatureResponse, error) {
	// Convert to SDK context
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Add domain separation for external requests by prepending CUSTOM_SIGNATURE_DOMAIN
	// This prevents unauthorized signatures from being created for unintended operations
	customData := make([][]byte, 0, len(msg.Data)+1)
	customData = append(customData, CUSTOM_SIGNATURE_DOMAIN)
	customData = append(customData, msg.Data...)

	// Create SigningData struct from the message with custom domain prefix
	signingData := types.SigningData{
		CurrentEpochId: msg.CurrentEpochId,
		ChainId:        msg.ChainId,
		RequestId:      msg.RequestId,
		Data:           customData,
	}

	// Call the core RequestThresholdSignature function which handles:
	// 1. Validates the request (epoch, uniqueness, etc.)
	// 2. Creates and stores the ThresholdSigningRequest
	// 3. Emits EventThresholdSigningRequested for controllers
	err := ms.Keeper.RequestThresholdSignature(sdkCtx, signingData)
	if err != nil {
		return nil, fmt.Errorf("failed to request threshold signature: %w", err)
	}

	return &types.MsgRequestThresholdSignatureResponse{}, nil
}
