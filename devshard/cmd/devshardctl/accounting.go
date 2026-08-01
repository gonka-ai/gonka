package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"devshard/accounting"
	"devshard/types"
	"devshard/user"
)

type gatewayAccountingRecorder struct {
	service *accounting.Service
}

func newGatewayAccountingRecorder(service *accounting.Service) *gatewayAccountingRecorder {
	if service == nil || service.Book == nil {
		return nil
	}
	service.SetPhaseProvider(currentAccountingPhase)
	return &gatewayAccountingRecorder{service: service}
}

func (r *gatewayAccountingRecorder) attachRuntime(rt *devshardRuntime) {
	if r == nil || rt == nil || rt.session == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return
	}
	stateSnapshot := rt.proxy.sm.SnapshotStateNoInferences()
	if err := r.service.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID:      rt.id,
		CreationEpoch: rt.session.EpochID(),
		Model:         rt.model,
		Slots:         stateSnapshot.Group,
		Phase:         accountingEscrowPhase(rt.proxy.sm.Phase()),
	}, stateSnapshot.Config, user.TimeoutBuffer); err != nil {
		log.Printf("gateway accounting register escrow=%s: %v", rt.id, err)
		return
	}
	var commitMu sync.Mutex
	enabled := true
	recordDiff := func(diff types.Diff) {
		commitMu.Lock()
		defer commitMu.Unlock()
		if !enabled {
			return
		}
		if err := r.service.RecordCommittedDiff(rt.id, diff, rt.proxy.sm); err != nil {
			log.Printf("gateway accounting committed diff escrow=%s nonce=%d: %v", rt.id, diff.Nonce, err)
		}
	}
	commitMu.Lock()
	tail, err := rt.session.SetCommittedDiffHook(r.service.Book.ReducedThrough(rt.id), recordDiff)
	if err != nil {
		commitMu.Unlock()
		log.Printf("gateway accounting recovery tail escrow=%s: %v", rt.id, err)
		return
	}
	for _, diff := range tail {
		if err := r.service.RecordRecoveredDiff(rt.id, diff, rt.proxy.sm); err != nil {
			log.Printf("gateway accounting recovery diff escrow=%s nonce=%d: %v", rt.id, diff.Nonce, err)
			enabled = false
			commitMu.Unlock()
			return
		}
	}
	if err := r.service.ReconcilePending(rt.id, rt.proxy.sm); err != nil {
		log.Printf("gateway accounting reconcile pending escrow=%s: %v", rt.id, err)
		enabled = false
		commitMu.Unlock()
		return
	}
	if err := r.service.RecordPhase(rt.id, accountingEscrowPhase(rt.proxy.sm.Phase())); err != nil {
		log.Printf("gateway accounting recovered phase escrow=%s: %v", rt.id, err)
		enabled = false
		commitMu.Unlock()
		return
	}
	commitMu.Unlock()
	if rt.proxy.redundancy != nil {
		rt.proxy.redundancy.setAccounting(r)
	}
	rt.accountingFlush = func() {
		if err := r.service.RecordPhase(rt.id, accountingEscrowPhase(rt.proxy.sm.Phase())); err != nil {
			log.Printf("gateway accounting phase escrow=%s: %v", rt.id, err)
		}
		if err := r.flush(context.Background()); err != nil {
			log.Printf("gateway accounting flush escrow=%s: %v", rt.id, err)
		}
	}
}

func (r *gatewayAccountingRecorder) recordEscrowPhase(escrowID string, phase types.SessionPhase) {
	if err := r.service.RecordPhase(escrowID, accountingEscrowPhase(phase)); err != nil {
		log.Printf("gateway accounting phase escrow=%s: %v", escrowID, err)
	}
}

func (r *gatewayAccountingRecorder) recordSettled(escrowID string) {
	if err := r.service.RecordPhase(escrowID, accounting.EscrowSettled); err != nil {
		log.Printf("gateway accounting settled escrow=%s: %v", escrowID, err)
	}
}

func (r *gatewayAccountingRecorder) setBlockHeightProvider(provider func() int64) {
	if r != nil && r.service != nil && r.service.Book != nil {
		r.service.Book.SetBlockHeightProvider(provider)
	}
}

func (r *gatewayAccountingRecorder) recordGhost(escrowID string, nonce uint64, reason, quarantine string) {
	noSend := accountingNoSendReason(reason)
	// A recognized no_send_reason already carries the whole story.
	detailReason := ""
	if noSend == accounting.NoSendUnknown {
		detailReason = reason
	}
	if err := r.service.RecordGhost(
		escrowID,
		nonce,
		currentAccountingPhase(),
		accountingQuarantine(quarantine),
		noSend,
		detailReason,
	); err != nil {
		log.Printf("gateway accounting ghost escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func accountingNoSendReason(reason string) accounting.NoSendReason {
	switch value := accounting.NoSendReason(reason); value {
	case accounting.NoSendPoCUnavailable, accounting.NoSendParticipantThrottled,
		accounting.NoSendParticipantCapability, accounting.NoSendNoCompatibleAfterStale:
		return value
	default:
		return accounting.NoSendUnknown
	}
}

func (r *gatewayAccountingRecorder) recordRealSend(
	escrowID string,
	nonce uint64,
	quarantine string,
	sendTime time.Time,
) {
	if sendTime.IsZero() {
		sendTime = time.Now()
	}
	if err := r.service.RecordRealSend(
		escrowID,
		nonce,
		currentAccountingPhase(),
		accountingQuarantine(quarantine),
		sendTime,
	); err != nil {
		log.Printf("gateway accounting real send escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *gatewayAccountingRecorder) recordUsage(escrowID string, nonce, winnerNonce uint64) {
	usage := accounting.UsageLoser
	switch {
	case winnerNonce == 0:
		usage = accounting.UsageUnknownValue
	case nonce == winnerNonce:
		usage = accounting.UsageWinner
	}
	if err := r.service.RecordUsage(escrowID, nonce, usage); err != nil {
		log.Printf("gateway accounting usage escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *gatewayAccountingRecorder) recordTimeout(
	escrowID string,
	nonce uint64,
	kind, action, reason, detailReason, timeoutReason string,
) {
	if action == "started" {
		return
	}
	if action == "skipped" && !timeoutSkipRequiresAccounting(reason) {
		return
	}
	var outcome accounting.TimeoutOutcome
	switch {
	case action == "completed":
		outcome = accounting.TimeoutApplied
	case action == "skipped":
		outcome = accounting.TimeoutSkipped
	case action == "failed":
		outcome = accounting.TimeoutOutcome(reason)
	}
	if outcome == "" {
		return
	}
	if err := r.service.RecordTimeoutOutcome(accounting.TimeoutRecord{
		EscrowID:      escrowID,
		Nonce:         nonce,
		Kind:          accounting.TimeoutKind(kind),
		Phase:         currentAccountingPhase(),
		Outcome:       outcome,
		Reason:        accountingTimeoutReason(outcome, timeoutReason),
		FailureOrigin: accountingFailureOrigin(detailReason),
		DetailReason:  detailReason,
	}); err != nil {
		log.Printf("gateway accounting timeout escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

// accountingTimeoutReason keeps the outcome out of the reason dimension. A skip
// must always name its cause, so an unlisted one stays visible as unknown;
// every other outcome is its own explanation and may carry no reason.
func accountingTimeoutReason(outcome accounting.TimeoutOutcome, reason string) accounting.TimeoutReason {
	switch value := accounting.TimeoutReason(reason); value {
	case accounting.TimeoutPhaseTransitionAborted, accounting.TimeoutLongResponseAfterContent,
		accounting.TimeoutStateRootDiverged, accounting.TimeoutContextCanceled,
		accounting.TimeoutDiffDeliveryFailed, accounting.TimeoutNotApplied:
		return value
	}
	if outcome == accounting.TimeoutSkipped {
		return accounting.TimeoutReasonUnknown
	}
	return ""
}

func timeoutSkipRequiresAccounting(reason string) bool {
	switch reason {
	case "nonce_already_finished", "empty_stream_without_non_empty_winner":
		return false
	default:
		return true
	}
}

func accountingFailureOrigin(reason string) accounting.FailureOrigin {
	switch {
	case reason == "context_canceled" || strings.Contains(reason, "client"):
		return accounting.FailureClient
	case reason == "phase_transition_aborted",
		reason == "long_response_after_content",
		reason == "timeout_not_applied",
		reason == "nonce_already_finished",
		strings.Contains(reason, "policy"):
		return accounting.FailureGatewayPolicy
	case reason == "not_finished",
		reason == "escrow_state_root_diverged",
		strings.Contains(reason, "http_"),
		strings.Contains(reason, "stream"),
		strings.Contains(reason, "response"):
		return accounting.FailureHostResponse
	default:
		return accounting.FailureTransportUnknown
	}
}

func (r *gatewayAccountingRecorder) flush(ctx context.Context) error {
	if r == nil || r.service == nil {
		return nil
	}
	return r.service.Flush(ctx)
}

func (r *gatewayAccountingRecorder) close() error {
	if r == nil || r.service == nil {
		return nil
	}
	return r.service.Close()
}

func accountingEscrowPhase(phase types.SessionPhase) accounting.EscrowPhase {
	switch phase {
	case types.PhaseFinalizing:
		return accounting.EscrowFinalizing
	case types.PhaseSettlement:
		return accounting.EscrowFinalized
	default:
		return accounting.EscrowActive
	}
}

func currentAccountingPhase() accounting.Phase {
	reason := currentPoCPhaseReason()
	switch {
	case reason == "confirmation_poc":
		return accounting.PhaseConfirmationPoC
	case reason != "":
		return accounting.PhasePoC
	default:
		return accounting.PhaseNormal
	}
}

func accountingQuarantine(value string) accounting.QuarantineMode {
	switch value {
	case "probe":
		return accounting.QuarantineProbe
	case "shadow":
		return accounting.QuarantineShadow
	case "probation":
		return accounting.QuarantineProbation
	default:
		return accounting.QuarantineNone
	}
}
