package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
)

// SubmitDealerPart handles the submission of dealer parts during the dealing phase of DKG.
//
// Persists the submitted dealer part under its own KV sub-key via SetDealerPart
// rather than rewriting the entire EpochBLSData struct. The read is the base
// epoch record plus this sender's sub-key. GetEpochBLSData would also scan
// every other dealer part, so gas would grow with parts that landed earlier
// in the block and a simulation taken at the start of dealing would be too small.
func (ms msgServer) SubmitDealerPart(goCtx context.Context, msg *types.MsgSubmitDealerPart) (*types.MsgSubmitDealerPartResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Phase, deadline, and the participant list live on the base record.
	// Dealer parts, verifications, and complaints stay unread.
	epochBLSData, err := ms.GetEpochBLSDataBase(ctx, msg.EpochId)
	if err != nil {
		return nil, fmt.Errorf("failed to get epoch %d BLS data: %w", msg.EpochId, err)
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

	// A pre-split base record may still carry this slot inline. Current
	// submissions are stored only under the sender's sub-key.
	if participantIndex < len(epochBLSData.DealerParts) {
		inline := epochBLSData.DealerParts[participantIndex]
		if inline != nil && inline.DealerAddress != "" {
			return nil, fmt.Errorf("participant %s has already submitted dealer part for epoch %d", msg.Creator, msg.EpochId)
		}
	}
	stored, err := ms.GetDealerPart(ctx, msg.EpochId, uint32(participantIndex))
	if err != nil {
		return nil, fmt.Errorf("failed to read dealer part for epoch %d participant %d: %w", msg.EpochId, participantIndex, err)
	}
	if stored != nil && stored.DealerAddress != "" {
		return nil, fmt.Errorf("participant %s has already submitted dealer part for epoch %d", msg.Creator, msg.EpochId)
	}

	// Validate that encrypted shares are provided for all participants
	if len(msg.EncryptedSharesForParticipants) != len(epochBLSData.Participants) {
		return nil, fmt.Errorf("expected encrypted shares for %d participants, got %d", len(epochBLSData.Participants), len(msg.EncryptedSharesForParticipants))
	}

	// Enforce fixed polynomial degree: commitments must contain exactly t+1 coefficients.
	expectedCommitmentsCount := int(epochBLSData.TSlotsDegree) + 1
	if len(msg.Commitments) != expectedCommitmentsCount {
		return nil, fmt.Errorf("expected %d commitments (t_slots_degree + 1), got %d", expectedCommitmentsCount, len(msg.Commitments))
	}

	// Enforce encrypted_shares shape for each recipient:
	// len(encrypted_shares) == recipient_slot_count * recipient_keys_per_slot.
	for i, participant := range epochBLSData.Participants {
		if err := validateEncryptedSharesShape(participant, msg.EncryptedSharesForParticipants[i].EncryptedShares); err != nil {
			return nil, fmt.Errorf("invalid encrypted shares for participant index %d: %w", i, err)
		}
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

	// Constant-cost write: only this dealer's sub-key is updated.
	if err := ms.SetDealerPart(ctx, msg.EpochId, uint32(participantIndex), dealerPart); err != nil {
		return nil, fmt.Errorf("failed to save dealer part for epoch %d, participant %d: %w", msg.EpochId, participantIndex, err)
	}

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
