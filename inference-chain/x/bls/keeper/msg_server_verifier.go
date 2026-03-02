package keeper

import (
	"context"
	"errors"
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
	epochBLSData, err := ms.GetEpochBLSData(sdkCtx, msg.EpochId)
	if err != nil {
		if errors.Is(err, types.ErrEpochBLSDataNotFound) {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("no DKG data found for epoch %d", msg.EpochId))
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to get epoch %d BLS data: %v", msg.EpochId, err))
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

	complaintsByDealer := make(map[uint32]types.VerificationDealerComplaint, len(msg.DealerComplaints))
	for _, complaint := range msg.DealerComplaints {
		if _, exists := complaintsByDealer[complaint.DealerIndex]; exists {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("duplicate dealer complaint for dealer index %d", complaint.DealerIndex))
		}
		complaintsByDealer[complaint.DealerIndex] = complaint
	}

	// Store verification submission at participant's index (same as dealer_parts pattern)
	epochBLSData.VerificationSubmissions[participantIndex] = &types.VerificationVectorSubmission{
		DealerValidity: msg.DealerValidity,
	}

	// Persist complaint evidence alongside verification vote. One complaint per (dealer, complainer).
	for dealerIndex, dealerValid := range msg.DealerValidity {
		if dealerValid {
			continue
		}

		complaint, hasComplaint := complaintsByDealer[uint32(dealerIndex)]
		requiresEvidence := false
		// Defense-in-depth: these shape checks should already hold after SubmitDealerPart +
		// MsgSubmitDealerPart.ValidateBasic. We re-check persisted epoch state here before
		// making complaint evidence mandatory, so inconsistent/legacy state does not cause
		// panics or force evidence requirements for malformed dealer entries.
		if dealerIndex < len(epochBLSData.DealerParts) {
			dealerPart := epochBLSData.DealerParts[dealerIndex]
			expectedCommitmentsCount := int(epochBLSData.TSlotsDegree) + 1
			if dealerPart != nil &&
				dealerPart.DealerAddress != "" &&
				len(dealerPart.Commitments) == expectedCommitmentsCount &&
				participantIndex < len(dealerPart.ParticipantShares) &&
				dealerPart.ParticipantShares[participantIndex] != nil &&
				len(dealerPart.ParticipantShares[participantIndex].EncryptedShares) > 0 {
				requiresEvidence = true
			}
		}
		if requiresEvidence && !hasComplaint {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("missing complaint evidence for voted-false dealer index %d", dealerIndex))
		}
		if !hasComplaint {
			continue
		}

		for _, existingComplaint := range epochBLSData.DealerComplaints {
			if existingComplaint.DealerIndex == uint32(dealerIndex) && existingComplaint.ComplainerIndex == uint32(participantIndex) {
				return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("complaint already exists for dealer %d by participant %s", dealerIndex, msg.Creator))
			}
		}

		epochBLSData.DealerComplaints = append(epochBLSData.DealerComplaints, types.DealerComplaint{
			DealerIndex:             uint32(dealerIndex),
			ComplainerIndex:         uint32(participantIndex),
			DisputedSlotIndex:       complaint.DisputedSlotIndex,
			DisputedCiphertextIndex: complaint.DisputedCiphertextIndex,
		})
	}

	// Store updated EpochBLSData
	if err := ms.SetEpochBLSData(sdkCtx, epochBLSData); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to store updated epoch %d BLS data: %v", msg.EpochId, err))
	}

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
