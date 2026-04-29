package keeper

import (
	"context"
	"errors"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	blst "github.com/supranational/blst/bindings/go"
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
	if currentHeight > epochBLSData.VerifyingPhaseDeadlineBlock {
		return nil, status.Error(codes.DeadlineExceeded, fmt.Sprintf("verification deadline passed: current height %d > deadline %d", currentHeight, epochBLSData.VerifyingPhaseDeadlineBlock))
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

	if err := ms.validateDealerValidityProofs(msg, &epochBLSData, participantIndex); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	complaintsByDealer := make(map[uint32]types.VerificationDealerComplaint, len(msg.DealerComplaints))
	for _, complaint := range msg.DealerComplaints {
		if _, exists := complaintsByDealer[complaint.DealerIndex]; exists {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("duplicate dealer complaint for dealer index %d", complaint.DealerIndex))
		}
		complaintsByDealer[complaint.DealerIndex] = complaint
	}

	// Persist the verification submission to its own sub-key. Write cost is
	// bounded by this participant's own DealerValidity payload, independent
	// of how many other verifiers have landed — the split prevents the
	// N^2 write-per-byte growth that caused later verifiers to hit
	// simulated-vs-actual out-of-gas in a full-sized round. The in-memory
	// epochBLSData is still updated below so the complaint-persistence
	// loop sees a consistent view of the current submission.
	submission := &types.VerificationVectorSubmission{
		DealerValidity: msg.DealerValidity,
	}
	if err := ms.SetVerificationSubmission(sdkCtx, msg.EpochId, uint32(participantIndex), submission); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to persist verification submission for epoch %d: %v", msg.EpochId, err))
	}
	epochBLSData.VerificationSubmissions[participantIndex] = submission

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
			participant := epochBLSData.Participants[participantIndex]
			if dealerPart != nil &&
				dealerPart.DealerAddress != "" &&
				len(dealerPart.Commitments) == expectedCommitmentsCount &&
				participantIndex < len(dealerPart.ParticipantShares) &&
				dealerPart.ParticipantShares[participantIndex] != nil &&
				len(dealerPart.ParticipantShares[participantIndex].EncryptedShares) > 0 &&
				hasValidEncryptedSharesShape(participant, dealerPart.ParticipantShares[participantIndex].EncryptedShares) {
				requiresEvidence = true
			}
		}
		if requiresEvidence && !hasComplaint {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("missing complaint evidence for voted-false dealer index %d", dealerIndex))
		}
		if !hasComplaint {
			continue
		}

		if err := validateComplaintIndices(&epochBLSData, dealerIndex, participantIndex, &complaint); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		// O(1) duplicate check via direct (dealer, complainer) sub-key
		// lookup, replacing the prior O(N) scan through the inline slice.
		if ms.HasDealerComplaint(sdkCtx, msg.EpochId, uint32(dealerIndex), uint32(participantIndex)) {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("complaint already exists for dealer %d by participant %s", dealerIndex, msg.Creator))
		}

		newComplaint := types.DealerComplaint{
			DealerIndex:             uint32(dealerIndex),
			ComplainerIndex:         uint32(participantIndex),
			DisputedSlotIndex:       complaint.DisputedSlotIndex,
			DisputedCiphertextIndex: complaint.DisputedCiphertextIndex,
		}
		if err := ms.SetDealerComplaint(sdkCtx, msg.EpochId, &newComplaint); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("failed to persist dealer complaint (%d,%d): %v", dealerIndex, participantIndex, err))
		}
	}

	// Store updated EpochBLSData. DealerParts, VerificationSubmissions, and
	// DealerComplaints are all already persisted in their per-entry
	// sub-keys (this verifier just wrote their own; every other
	// participant's entries are already in the sub-key store from their
	// earlier tx). Null them out here so SetEpochBLSData's sync loops
	// don't redundantly rewrite every sub-key on every verifier's tx,
	// which would reintroduce O(N) writes per submission.
	epochBLSData.DealerParts = nil
	epochBLSData.VerificationSubmissions = nil
	epochBLSData.DealerComplaints = nil
	if err := ms.SetEpochBLSData(sdkCtx, epochBLSData); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to store updated epoch %d BLS data: %v", msg.EpochId, err))
	}

	// Emit EventVerificationVectorSubmitted
	event := types.EventVerificationVectorSubmitted{
		EpochId:            msg.EpochId,
		ParticipantAddress: msg.Creator,
	}

	if err := sdkCtx.EventManager().EmitTypedEvent(&event); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to emit verification vector submitted event for epoch %d: %v", msg.EpochId, err))
	}

	ms.Logger().Info(
		"Verification vector submitted",
		"epoch_id", msg.EpochId,
		"participant", msg.Creator,
		"dealer_validity_count", len(msg.DealerValidity),
	)

	return &types.MsgSubmitVerificationVectorResponse{}, nil
}

func (ms msgServer) validateDealerValidityProofs(msg *types.MsgSubmitVerificationVector, epochBLSData *types.EpochBLSData, participantIndex int) error {
	trueNonSelfDealerCount := 0
	selfMarkedTrue := participantIndex >= 0 && participantIndex < len(msg.DealerValidity) && msg.DealerValidity[participantIndex]
	for dealerIndex, isValid := range msg.DealerValidity {
		if isValid && dealerIndex != participantIndex {
			trueNonSelfDealerCount++
		}
	}

	maxProofCount := trueNonSelfDealerCount
	if selfMarkedTrue {
		// Self proof is optional: self-vote is excluded from weighted quorum anyway.
		maxProofCount++
	}
	if len(msg.DealerValidityProofs) < trueNonSelfDealerCount || len(msg.DealerValidityProofs) > maxProofCount {
		if selfMarkedTrue {
			return fmt.Errorf(
				"dealer_validity_proofs count %d is invalid: expected %d (without self proof) or %d (with optional self proof)",
				len(msg.DealerValidityProofs),
				trueNonSelfDealerCount,
				maxProofCount,
			)
		}
		return fmt.Errorf(
			"dealer_validity_proofs count %d does not match true non-self dealer count %d",
			len(msg.DealerValidityProofs),
			trueNonSelfDealerCount,
		)
	}

	// Index proofs by dealer index to keep validation path deterministic and allocation-light
	proofsByDealer := make([]*types.DealerValidityProof, len(msg.DealerValidity))
	for i := range msg.DealerValidityProofs {
		proof := &msg.DealerValidityProofs[i]
		if proof.DealerIndex >= uint32(len(epochBLSData.Participants)) {
			return fmt.Errorf("dealer_validity_proofs[%d].dealer_index %d out of range", i, proof.DealerIndex)
		}
		if len(proof.ProofSignature) == 0 {
			return fmt.Errorf("dealer_validity_proofs[%d].proof_signature must be non-empty", i)
		}
		dealerIndex := int(proof.DealerIndex)
		if dealerIndex >= len(msg.DealerValidity) || !msg.DealerValidity[dealerIndex] {
			return fmt.Errorf("proof provided for dealer %d that is marked false", proof.DealerIndex)
		}
		if proofsByDealer[dealerIndex] != nil {
			return fmt.Errorf("duplicate proof for dealer %d", proof.DealerIndex)
		}
		proofsByDealer[dealerIndex] = proof
	}

	slotIndices, err := participantSlotIndices(epochBLSData, participantIndex)
	if err != nil {
		return err
	}

	// Deterministic iteration: walk dealer_validity by index
	for dealerIndex, isValid := range msg.DealerValidity {
		if !isValid {
			continue
		}
		if dealerIndex == participantIndex {
			continue
		}

		proof := proofsByDealer[dealerIndex]
		if proof == nil {
			return fmt.Errorf("missing proof for dealer %d", dealerIndex)
		}
		if err := ms.verifyDealerValidityProof(epochBLSData, dealerIndex, slotIndices, proof.ProofSignature); err != nil {
			return fmt.Errorf("invalid proof for dealer %d: %w", dealerIndex, err)
		}
	}

	return nil
}

func participantSlotIndices(epochBLSData *types.EpochBLSData, participantIndex int) ([]uint32, error) {
	if participantIndex < 0 || participantIndex >= len(epochBLSData.Participants) {
		return nil, fmt.Errorf("participant index %d out of range", participantIndex)
	}

	participant := epochBLSData.Participants[participantIndex]
	if participant.SlotEndIndex < participant.SlotStartIndex {
		return nil, fmt.Errorf("invalid slot range for participant %d in epoch %d", participantIndex, epochBLSData.EpochId)
	}

	slotCount := participant.SlotEndIndex - participant.SlotStartIndex + 1
	slotIndices := make([]uint32, 0, int(slotCount))
	for slot := participant.SlotStartIndex; slot <= participant.SlotEndIndex; slot++ {
		slotIndices = append(slotIndices, slot)
	}

	return slotIndices, nil
}

func validateComplaintIndices(epochBLSData *types.EpochBLSData, dealerIndex, complainerIndex int, complaint *types.VerificationDealerComplaint) error {
	if complaint == nil {
		return fmt.Errorf("complaint cannot be nil")
	}
	if complainerIndex < 0 || complainerIndex >= len(epochBLSData.Participants) {
		return fmt.Errorf("complainer index %d out of range", complainerIndex)
	}
	if dealerIndex == complainerIndex {
		return fmt.Errorf("self complaint is not allowed for dealer %d", dealerIndex)
	}

	participant := epochBLSData.Participants[complainerIndex]
	if participant.SlotEndIndex < participant.SlotStartIndex {
		return fmt.Errorf("invalid slot range for participant %d in epoch %d", complainerIndex, epochBLSData.EpochId)
	}
	if complaint.DisputedSlotIndex < participant.SlotStartIndex || complaint.DisputedSlotIndex > participant.SlotEndIndex {
		return fmt.Errorf("disputed_slot_index %d out of range for participant %d", complaint.DisputedSlotIndex, complainerIndex)
	}

	if dealerIndex < 0 || dealerIndex >= len(epochBLSData.DealerParts) || epochBLSData.DealerParts[dealerIndex] == nil {
		return fmt.Errorf("dealer index %d has no dealer part", dealerIndex)
	}
	dealerPart := epochBLSData.DealerParts[dealerIndex]
	if complainerIndex >= len(dealerPart.ParticipantShares) || dealerPart.ParticipantShares[complainerIndex] == nil {
		return fmt.Errorf("dealer %d has no shares for participant %d", dealerIndex, complainerIndex)
	}

	encryptedShares := dealerPart.ParticipantShares[complainerIndex].EncryptedShares
	numSlots := int(participant.SlotEndIndex-participant.SlotStartIndex) + 1
	if numSlots <= 0 || len(encryptedShares) == 0 || len(encryptedShares)%numSlots != 0 {
		return fmt.Errorf("invalid encrypted shares shape for dealer %d and participant %d", dealerIndex, complainerIndex)
	}

	keysPerSlot := len(encryptedShares) / numSlots
	slotOffset := int(complaint.DisputedSlotIndex - participant.SlotStartIndex)
	slotStart := slotOffset * keysPerSlot
	slotEnd := slotStart + keysPerSlot
	ciphertextIndex := int(complaint.DisputedCiphertextIndex)
	if ciphertextIndex < slotStart || ciphertextIndex >= slotEnd {
		return fmt.Errorf("disputed_ciphertext_index %d out of range for disputed_slot_index %d", complaint.DisputedCiphertextIndex, complaint.DisputedSlotIndex)
	}

	return nil
}

func (ms msgServer) verifyDealerValidityProof(epochBLSData *types.EpochBLSData, dealerIndex int, slotIndices []uint32, proofSignature []byte) error {
	if dealerIndex < 0 || dealerIndex >= len(epochBLSData.Participants) {
		return fmt.Errorf("dealer index %d out of range", dealerIndex)
	}
	if dealerIndex >= len(epochBLSData.DealerParts) || epochBLSData.DealerParts[dealerIndex] == nil || len(epochBLSData.DealerParts[dealerIndex].Commitments) == 0 {
		return fmt.Errorf("dealer %d has no commitments", dealerIndex)
	}

	epochForProof := *epochBLSData
	slotPublicKeys, err := buildDealerSlotPublicKeysForSlots(
		epochBLSData.DealerParts[dealerIndex].Commitments,
		epochBLSData.ITotalSlots,
		slotIndices,
	)
	if err != nil {
		return fmt.Errorf("failed to precompute dealer slot public keys: %w", err)
	}
	epochForProof.SlotPublicKeys = slotPublicKeys

	proofHash := types.BuildDealerValidityProofHash(epochBLSData.EpochId, uint32(dealerIndex))
	if !ms.verifyBLSPartialSignatureBlst(proofSignature, proofHash, &epochForProof, slotIndices) {
		return fmt.Errorf("BLS proof signature verification failed")
	}

	return nil
}

func buildDealerSlotPublicKeysForSlots(commitments [][]byte, totalSlots uint32, slotIndices []uint32) ([][]byte, error) {
	if len(commitments) == 0 {
		return nil, fmt.Errorf("commitments must be non-empty")
	}

	commitmentPoints := make([]*blst.P2Affine, len(commitments))
	for i, commitmentBytes := range commitments {
		point := new(blst.P2Affine).Uncompress(commitmentBytes)
		if point == nil {
			return nil, fmt.Errorf("failed to unmarshal commitment %d with blst", i)
		}
		if !point.InG2() {
			return nil, fmt.Errorf("commitment %d is not in G2 subgroup", i)
		}
		commitmentPoints[i] = point
	}

	slotPublicKeys := make([][]byte, totalSlots)
	for _, slotIndex := range slotIndices {
		if slotIndex >= totalSlots {
			return nil, fmt.Errorf("slot index %d out of range for total slots %d", slotIndex, totalSlots)
		}

		var x fr.Element
		x.SetUint64(uint64(slotIndex + 1))

		var power fr.Element
		power.SetOne()

		scalars := make([]byte, len(commitmentPoints)*32)
		for i := 0; i < len(commitmentPoints); i++ {
			powerBytes := power.Bytes()
			// gnark uses big-endian; blst expects little-endian scalar bytes.
			for j := 0; j < 16; j++ {
				powerBytes[j], powerBytes[31-j] = powerBytes[31-j], powerBytes[j]
			}
			copy(scalars[i*32:(i+1)*32], powerBytes[:])
			power.Mul(&power, &x)
		}

		slotPublicKey := blst.P2AffinesMult(commitmentPoints, scalars, 255)
		slotPublicKeys[slotIndex] = slotPublicKey.ToAffine().Compress()
	}

	return slotPublicKeys, nil
}
