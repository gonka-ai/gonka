package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/bls/types"
)

// SubmitVerificationVector handles verification vector submissions during the verifying phase
func (ms msgServer) SubmitVerificationVector(ctx context.Context, msg *types.MsgSubmitVerificationVector) (*types.MsgSubmitVerificationVectorResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Retrieve EpochBLSData for the requested epoch
	epochBLSData, found := ms.GetEpochBLSData(sdkCtx, msg.EpochId)
	if !found {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("no DKG data found for epoch %d", msg.EpochId))
	}

	// Verify current DKG phase is VERIFYING
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("DKG phase is %s, expected VERIFYING", epochBLSData.DkgPhase.String()))
	}

	// Verify current block height is before verification deadline
	currentHeight := sdkCtx.BlockHeight()
	if currentHeight >= epochBLSData.VerifyingPhaseDeadlineBlock {
		return nil, status.Error(codes.DeadlineExceeded, fmt.Sprintf("verification deadline passed: current height %d >= deadline %d", currentHeight, epochBLSData.VerifyingPhaseDeadlineBlock))
	}

	// Find the participant in the participants list
	participantIndex := -1
	for i, participant := range epochBLSData.Participants {
		if participant.Address == msg.Creator {
			participantIndex = i
			break
		}
	}

	if participantIndex == -1 {
		return nil, status.Error(codes.PermissionDenied, fmt.Sprintf("address %s is not a participant in epoch %d", msg.Creator, msg.EpochId))
	}

	// Verify participant has not already submitted verification using dealer_validity length
	if len(epochBLSData.VerificationSubmissions[participantIndex].DealerValidity) > 0 {
		return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("participant %s has already submitted verification vector for epoch %d", msg.Creator, msg.EpochId))
	}

	// Verify dealer_validity array length matches number of participants
	if len(msg.DealerValidity) != len(epochBLSData.Participants) {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("dealer_validity length %d does not match participants count %d", len(msg.DealerValidity), len(epochBLSData.Participants)))
	}

	// Store verification submission at participant's index (same as dealer_parts pattern)
	epochBLSData.VerificationSubmissions[participantIndex] = &types.VerificationVectorSubmission{
		DealerValidity: msg.DealerValidity,
	}

	// Store updated EpochBLSData
	ms.SetEpochBLSData(sdkCtx, epochBLSData)

	// Emit EventVerificationVectorSubmitted
	event := types.EventVerificationVectorSubmitted{
		EpochId:            msg.EpochId,
		ParticipantAddress: msg.Creator,
	}

	sdkCtx.EventManager().EmitTypedEvent(&event)

	ms.Logger().Info(
		"Verification vector submitted",
		"epoch_id", msg.EpochId,
		"participant", msg.Creator,
		"dealer_validity_count", len(msg.DealerValidity),
	)

	return &types.MsgSubmitVerificationVectorResponse{}, nil
}
