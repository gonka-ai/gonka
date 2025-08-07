package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
)

// SubmitDealerPart handles the submission of dealer parts during the dealing phase of DKG
func (ms msgServer) SubmitDealerPart(goCtx context.Context, msg *types.MsgSubmitDealerPart) (*types.MsgSubmitDealerPartResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Get the epoch BLS data
	epochBLSData, found := ms.GetEpochBLSData(ctx, msg.EpochId)
	if !found {
		return nil, fmt.Errorf("epoch %d not found", msg.EpochId)
	}

	// Check if DKG is in dealing phase
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_DEALING {
		return nil, fmt.Errorf("DKG for epoch %d is not in dealing phase (current phase: %s)", msg.EpochId, epochBLSData.DkgPhase.String())
	}

	// Check if dealing phase deadline has passed
	if ctx.BlockHeight() > epochBLSData.DealingPhaseDeadlineBlock {
		return nil, fmt.Errorf("dealing phase deadline has passed for epoch %d", msg.EpochId)
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
		return nil, fmt.Errorf("creator %s is not a participant in epoch %d", msg.Creator, msg.EpochId)
	}

	// Check if this participant has already submitted their dealer part
	if epochBLSData.DealerParts[participantIndex] != nil && epochBLSData.DealerParts[participantIndex].DealerAddress != "" {
		return nil, fmt.Errorf("participant %s has already submitted dealer part for epoch %d", msg.Creator, msg.EpochId)
	}

	// Validate that encrypted shares are provided for all participants
	if len(msg.EncryptedSharesForParticipants) != len(epochBLSData.Participants) {
		return nil, fmt.Errorf("expected encrypted shares for %d participants, got %d", len(epochBLSData.Participants), len(msg.EncryptedSharesForParticipants))
	}

	// Create dealer part storage
	participantShares := make([]*types.EncryptedSharesForParticipant, len(msg.EncryptedSharesForParticipants))
	for i := range msg.EncryptedSharesForParticipants {
		participantShares[i] = &msg.EncryptedSharesForParticipants[i]
	}

	dealerPart := &types.DealerPartStorage{
		DealerAddress:     msg.Creator,
		Commitments:       msg.Commitments,
		ParticipantShares: participantShares,
	}

	// Store the dealer part
	epochBLSData.DealerParts[participantIndex] = dealerPart

	// Save the updated epoch BLS data
	ms.SetEpochBLSData(ctx, epochBLSData)

	// Emit EventDealerPartSubmitted
	event := &types.EventDealerPartSubmitted{
		EpochId:       msg.EpochId,
		DealerAddress: msg.Creator,
	}

	if err := ctx.EventManager().EmitTypedEvent(event); err != nil {
		ms.Logger().Error("Failed to emit EventDealerPartSubmitted", "error", err)
	}

	ms.Logger().Info(
		"Dealer part submitted",
		"epoch_id", msg.EpochId,
		"dealer", msg.Creator,
		"commitments_count", len(msg.Commitments),
	)

	return &types.MsgSubmitDealerPartResponse{}, nil
}
