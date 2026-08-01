package accounting

import (
	"errors"
	"fmt"
	"strings"

	"devshard/types"
)

func normalizeMetadata(metadata EscrowMetadata) (EscrowMetadata, error) {
	metadata.EscrowID = strings.TrimSpace(metadata.EscrowID)
	metadata.Model = strings.TrimSpace(metadata.Model)
	if metadata.EscrowID == "" {
		return EscrowMetadata{}, errors.New("escrow id is required")
	}
	if metadata.Model == "" {
		return EscrowMetadata{}, errors.New("model is required")
	}
	if err := types.ValidateGroup(metadata.Slots); err != nil {
		return EscrowMetadata{}, fmt.Errorf("invalid escrow slots: %w", err)
	}
	if metadata.Phase == "" {
		metadata.Phase = EscrowActive
	}
	if !validEscrowPhase(metadata.Phase) {
		return EscrowMetadata{}, fmt.Errorf("invalid escrow phase %q", metadata.Phase)
	}
	metadata.Slots = append([]types.SlotAssignment(nil), metadata.Slots...)
	return metadata, nil
}

func sameMetadata(left, right EscrowMetadata) bool {
	if left.EscrowID != right.EscrowID || left.CreationEpoch != right.CreationEpoch ||
		left.Model != right.Model || len(left.Slots) != len(right.Slots) {
		return false
	}
	for i := range left.Slots {
		if left.Slots[i] != right.Slots[i] {
			return false
		}
	}
	return true
}

func normalizePhase(phase Phase) (Phase, error) {
	if phase == "" {
		return PhaseNormal, nil
	}
	switch phase {
	case PhaseNormal, PhasePoC, PhaseConfirmationPoC:
		return phase, nil
	default:
		return "", fmt.Errorf("invalid accounting phase %q", phase)
	}
}

func normalizeQuarantine(mode QuarantineMode) (QuarantineMode, error) {
	if mode == "" {
		return QuarantineNone, nil
	}
	switch mode {
	case QuarantineNone, QuarantineProbe, QuarantineShadow, QuarantineProbation:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid quarantine mode %q", mode)
	}
}

func normalizeNoSendReason(reason NoSendReason) NoSendReason {
	switch reason {
	case NoSendPoCUnavailable, NoSendParticipantThrottled, NoSendParticipantCapability,
		NoSendNoCompatibleAfterStale:
		return reason
	default:
		return NoSendUnknown
	}
}

func normalizeFailureOrigin(origin FailureOrigin) (FailureOrigin, error) {
	if origin == "" {
		return FailureTransportUnknown, nil
	}
	switch origin {
	case FailureHostResponse, FailureGatewayPolicy, FailureClient, FailureTransportUnknown:
		return origin, nil
	default:
		return "", fmt.Errorf("invalid failure origin %q", origin)
	}
}

func normalizeDetailReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	switch reason {
	case "phase_transition_aborted",
		"error_stream",
		"empty_stream",
		"sse_truncated",
		"eof_transport",
		"client_cancelled",
		"transport_error",
		"no_receipt",
		"not_finished",
		"http_429",
		"http_503",
		"http_forbidden",
		"http_not_found",
		"http_timestamp_drift",
		"http_error",
		"long_response_after_content",
		"escrow_state_root_diverged",
		"context_canceled",
		"timeout_diff_delivery_failed",
		"timeout_not_applied",
		"poc_unavailable_host",
		"participant_throttled_no_send",
		"participant_capability_no_send",
		"no_compatible_request_after_stale":
		return reason
	default:
		return "unknown"
	}
}

func normalizeTimeoutReason(reason TimeoutReason) TimeoutReason {
	switch reason {
	case "":
		return ""
	case TimeoutPhaseTransitionAborted, TimeoutLongResponseAfterContent,
		TimeoutStateRootDiverged, TimeoutContextCanceled,
		TimeoutDiffDeliveryFailed, TimeoutNotApplied:
		return reason
	default:
		return TimeoutReasonUnknown
	}
}

func validTimeoutOutcome(outcome TimeoutOutcome) bool {
	switch outcome {
	case TimeoutSkipped, TimeoutVoteCollectionFailed, TimeoutInsufficientVotes,
		TimeoutDiffSendFailed, TimeoutApplied:
		return true
	default:
		return false
	}
}

func validEscrowPhase(phase EscrowPhase) bool {
	return escrowPhaseRank(phase) > 0
}

func escrowPhaseRank(phase EscrowPhase) int {
	switch phase {
	case EscrowActive:
		return 1
	case EscrowFinalizing:
		return 2
	case EscrowFinalized:
		return 3
	case EscrowSettled:
		return 4
	default:
		return 0
	}
}

func AssignedNonceSlot(nonce, slotCount uint64) uint32 {
	if nonce == 0 || slotCount == 0 {
		return 0
	}
	return uint32(nonce % slotCount)
}

// AssignedNoncesForSlot is identical to the chain settlement upper-bound
// formula. Slot 0 first receives nonce slotCount; slot k first receives nonce k.
func AssignedNoncesForSlot(latestNonce, slotCount uint64, slotID uint32) (uint64, error) {
	if slotCount == 0 {
		return 0, errors.New("slot count cannot be zero")
	}
	if uint64(slotID) >= slotCount {
		return 0, fmt.Errorf("slot %d out of range for slot count %d", slotID, slotCount)
	}
	firstAssignedNonce := uint64(slotID)
	if slotID == 0 {
		firstAssignedNonce = slotCount
	}
	if latestNonce < firstAssignedNonce {
		return 0, nil
	}
	return 1 + (latestNonce-firstAssignedNonce)/slotCount, nil
}

func cloneMetadata(metadata EscrowMetadata) EscrowMetadata {
	metadata.Slots = append([]types.SlotAssignment(nil), metadata.Slots...)
	return metadata
}
