package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
)

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
