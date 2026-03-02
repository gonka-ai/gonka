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

// RespondDealerComplaints stores dealer response payloads for existing complaints in a single transaction.
func (ms msgServer) RespondDealerComplaints(ctx context.Context, msg *types.MsgRespondDealerComplaints) (*types.MsgRespondDealerComplaintsResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	epochBLSData, err := ms.GetEpochBLSData(sdkCtx, msg.EpochId)
	if err != nil {
		if errors.Is(err, types.ErrEpochBLSDataNotFound) {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("no DKG data found for epoch %d", msg.EpochId))
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to get epoch %d BLS data: %v", msg.EpochId, err))
	}

	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_DISPUTING {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("DKG phase is %s, expected DISPUTING", epochBLSData.DkgPhase.String()))
	}

	currentHeight := sdkCtx.BlockHeight()
	if currentHeight >= epochBLSData.DisputingPhaseDeadlineBlock {
		return nil, status.Error(codes.DeadlineExceeded, fmt.Sprintf("disputing deadline passed: current height %d >= deadline %d", currentHeight, epochBLSData.DisputingPhaseDeadlineBlock))
	}

	if int(msg.DealerIndex) >= len(epochBLSData.Participants) {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("dealer_index %d is out of range for participants count %d", msg.DealerIndex, len(epochBLSData.Participants)))
	}

	dealerParticipant := epochBLSData.Participants[msg.DealerIndex]
	if dealerParticipant.Address != msg.Creator {
		return nil, status.Error(codes.PermissionDenied, fmt.Sprintf("address %s is not dealer index %d for epoch %d", msg.Creator, msg.DealerIndex, msg.EpochId))
	}

	if len(msg.Responses) == 0 {
		return nil, status.Error(codes.InvalidArgument, "responses must be non-empty")
	}

	for _, response := range msg.Responses {
		if int(response.ComplainerIndex) >= len(epochBLSData.Participants) {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("complainer_index %d is out of range for participants count %d", response.ComplainerIndex, len(epochBLSData.Participants)))
		}
		if len(response.ResponseShareBytes) != dkgShareBytesLen {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("response_share_bytes must be exactly %d bytes", dkgShareBytesLen))
		}
		if len(response.ResponseOpeningMaterial) != dkgOpeningSeedLen {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("response_opening_material must be exactly %d bytes", dkgOpeningSeedLen))
		}

		complaintIndex := -1
		for i, complaint := range epochBLSData.DealerComplaints {
			if complaint.DealerIndex == msg.DealerIndex && complaint.ComplainerIndex == response.ComplainerIndex {
				complaintIndex = i
				break
			}
		}
		if complaintIndex == -1 {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("complaint not found for dealer %d and complainer %d in epoch %d", msg.DealerIndex, response.ComplainerIndex, msg.EpochId))
		}

		complaint := epochBLSData.DealerComplaints[complaintIndex]
		if complaint.ResponseSubmitted {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("response already submitted for dealer %d and complainer %d in epoch %d", msg.DealerIndex, response.ComplainerIndex, msg.EpochId))
		}

		complaint.ResponseSubmitted = true
		complaint.ResponseShareBytes = response.ResponseShareBytes
		complaint.ResponseOpeningMaterial = response.ResponseOpeningMaterial
		epochBLSData.DealerComplaints[complaintIndex] = complaint
	}

	if err := ms.SetEpochBLSData(sdkCtx, epochBLSData); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to store updated epoch %d BLS data: %v", msg.EpochId, err))
	}

	for _, response := range msg.Responses {
		if err := sdkCtx.EventManager().EmitTypedEvent(&types.EventDealerComplaintResponded{
			EpochId:          msg.EpochId,
			DealerIndex:      msg.DealerIndex,
			ComplainerIndex:  response.ComplainerIndex,
			ResponderAddress: msg.Creator,
		}); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("failed to emit EventDealerComplaintResponded for epoch %d: %v", msg.EpochId, err))
		}
	}

	ms.Logger().Info(
		"Dealer complaint responses submitted",
		"epoch_id", msg.EpochId,
		"dealer", msg.Creator,
		"dealer_index", msg.DealerIndex,
		"responses_count", len(msg.Responses),
	)

	return &types.MsgRespondDealerComplaintsResponse{}, nil
}
