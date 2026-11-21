package keeper

import (
	"context"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
)

// SubmitDealerPart handles the submission of dealer parts during the dealing phase of DKG.
//
// Persists the submitted dealer part under its own KV sub-key via SetDealerPart
// rather than rewriting the entire EpochBLSData struct. Previously, each dealer
// submission rewrote EpochBLSData with the growing DealerParts slice inline,
// which meant the Nth dealer paid gas roughly N times the first dealer's cost.
// Between the DAPI's simulation-time gas estimate and block execution, more
// dealer parts could land, pushing the real write cost above the estimate and
// failing later dealers with "out of gas" — which in turn dropped them from the
// signing group and could cause the whole DKG round to fail. Per-sub-key writes
// make every dealer pay the same constant gas regardless of submission order.
func (ms msgServer) SubmitDealerPart(goCtx context.Context, msg *types.MsgSubmitDealerPart) (*types.MsgSubmitDealerPartResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Phase, deadline, and participant list live on the base record; avoid
	// rehydrating every other participant's dealer part on each submission.
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

	alreadySubmitted, err := ms.HasSubmittedDealerPart(ctx, msg.EpochId, &epochBLSData, participantIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to check dealer part for epoch %d participant %d: %w", msg.EpochId, participantIndex, err)
	}
	if alreadySubmitted {
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

	if err := ms.validateDealerCommitmentsAndProofs(ctx, &epochBLSData, msg); err != nil {
		return nil, err
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

func (ms msgServer) validateDealerCommitmentsAndProofs(
	ctx sdk.Context,
	epochBLSData *types.EpochBLSData,
	msg *types.MsgSubmitDealerPart,
) error {
	commitmentPoints, err := types.VerifyAndDecodeCommitmentKnowledgeProofs(
		ctx.ChainID(),
		msg.EpochId,
		msg.Creator,
		msg.Commitments,
		msg.CommitmentProofs,
	)
	if err != nil {
		return fmt.Errorf("invalid commitment knowledge proof: %w", err)
	}

	// commitmentPoints are already validated to lie in G2 by
	// VerifyAndDecodeCommitmentKnowledgeProofs, so any linear combination is
	// also in G2; only the projective identity check is needed per slot.
	var identityP2 blst.P2
	scalars := make([]byte, len(commitmentPoints)*32)
	for slotIndex := uint32(0); slotIndex < epochBLSData.ITotalSlots; slotIndex++ {
		var x, power fr.Element
		x.SetUint64(uint64(slotIndex + 1))
		power.SetOne()

		for i := 0; i < len(commitmentPoints); i++ {
			powerBytes := power.Bytes()
			for j := 0; j < 16; j++ {
				powerBytes[j], powerBytes[31-j] = powerBytes[31-j], powerBytes[j]
			}
			copy(scalars[i*32:(i+1)*32], powerBytes[:])
			power.Mul(&power, &x)
		}

		slotPubKeyP2 := blst.P2AffinesMult(commitmentPoints, scalars, 255)
		if slotPubKeyP2.Equals(&identityP2) {
			return fmt.Errorf("dealer commitment polynomial evaluates to identity at slot %d", slotIndex)
		}
	}

	return nil
}
