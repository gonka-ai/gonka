package main

import (
	"context"
	"log"

	"devshard/accounting"
	"devshard/state"
	"devshard/types"
)

type gatewayAccountingRecorder struct {
	tracker *accounting.Tracker
}

func newGatewayAccountingRecorder(tracker *accounting.Tracker) *gatewayAccountingRecorder {
	if tracker == nil {
		return nil
	}
	return &gatewayAccountingRecorder{tracker: tracker}
}

func (r *gatewayAccountingRecorder) attachRuntime(rt *devshardRuntime) {
	if r == nil || rt == nil || rt.session == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return
	}
	stateSnapshot := rt.proxy.sm.SnapshotStateNoInferences()
	if err := r.tracker.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID:      rt.id,
		CreationEpoch: rt.session.EpochID(),
		Model:         rt.model,
		Slots:         stateSnapshot.Group,
		Phase:         accountingEscrowPhase(rt.proxy.sm.Phase()),
	}); err != nil {
		log.Printf("gateway accounting register escrow=%s: %v", rt.id, err)
		return
	}
	for slot, stats := range stateSnapshot.HostStats {
		if stats != nil {
			_ = r.tracker.RecordHostStats(rt.id, slot, *stats)
		}
	}
	_ = r.tracker.ReconcileAppliedMisses(rt.id)
	rt.proxy.sm.SetAccountingObserver(func(event state.AccountingEvent) {
		r.recordStateEvent(rt.id, event)
	})
	if rt.proxy.redundancy != nil {
		rt.proxy.redundancy.accounting = r
	}
	rt.accountingFlush = func() {
		if err := r.tracker.RecordPhase(rt.id, accountingEscrowPhase(rt.proxy.sm.Phase())); err != nil {
			log.Printf("gateway accounting phase escrow=%s: %v", rt.id, err)
		}
		if err := r.flush(context.Background()); err != nil {
			log.Printf("gateway accounting flush escrow=%s: %v", rt.id, err)
		}
	}
}

func (r *gatewayAccountingRecorder) recordStateEvent(escrowID string, event state.AccountingEvent) {
	if event.DiffNonce != 0 {
		if err := r.tracker.RecordDiff(escrowID, event.DiffNonce, event.HasStartInference); err != nil {
			log.Printf("gateway accounting diff escrow=%s nonce=%d: %v", escrowID, event.DiffNonce, err)
		}
		return
	}
	if event.Kind != "" {
		if err := r.tracker.RecordProtocol(
			escrowID,
			event.InferenceID,
			event.ExecutorSlot,
			accounting.ProtocolKind(event.Kind),
			event.HostStats,
		); err != nil {
			log.Printf("gateway accounting protocol escrow=%s nonce=%d: %v", escrowID, event.InferenceID, err)
		}
	}
}

func (r *gatewayAccountingRecorder) recordEscrowPhase(escrowID string, phase types.SessionPhase) {
	if err := r.tracker.RecordPhase(escrowID, accountingEscrowPhase(phase)); err != nil {
		log.Printf("gateway accounting phase escrow=%s: %v", escrowID, err)
	}
}

func (r *gatewayAccountingRecorder) recordSettled(escrowID string) {
	if err := r.tracker.RecordPhase(escrowID, accounting.EscrowSettled); err != nil {
		log.Printf("gateway accounting settled escrow=%s: %v", escrowID, err)
	}
}

func (r *gatewayAccountingRecorder) recordGhost(escrowID string, nonce uint64, reason, quarantine string) {
	noSend := accounting.NoSendReasonFromString(reason)
	// A recognized no_send_reason already carries the whole story.
	detailReason := ""
	if noSend == accounting.NoSendUnknown {
		detailReason = reason
	}
	if err := r.tracker.RecordGhost(
		escrowID,
		nonce,
		currentAccountingPhase(),
		accounting.QuarantineFromString(quarantine),
		noSend,
		detailReason,
	); err != nil {
		log.Printf("gateway accounting ghost escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *gatewayAccountingRecorder) recordRealSend(
	escrowID string,
	nonce uint64,
	quarantine string,
) {
	if err := r.tracker.RecordRealSend(
		escrowID,
		nonce,
		currentAccountingPhase(),
		accounting.QuarantineFromString(quarantine),
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
	if err := r.tracker.RecordUsage(escrowID, nonce, usage); err != nil {
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
	outcome := accounting.TimeoutOutcomeFromAction(action, reason)
	if outcome == "" {
		return
	}
	if err := r.tracker.RecordTimeout(accounting.TimeoutRecord{
		EscrowID:      escrowID,
		Nonce:         nonce,
		Kind:          accounting.TimeoutKind(kind),
		Phase:         currentAccountingPhase(),
		Outcome:       outcome,
		Reason:        accounting.TimeoutReasonFromString(outcome, timeoutReason),
		FailureOrigin: accounting.FailureOriginFromDetail(detailReason),
		DetailReason:  detailReason,
	}); err != nil {
		log.Printf("gateway accounting timeout escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func timeoutSkipRequiresAccounting(reason string) bool {
	switch reason {
	case "nonce_already_finished", "empty_stream_without_non_empty_winner":
		return false
	default:
		return true
	}
}

func (r *gatewayAccountingRecorder) flush(ctx context.Context) error {
	if r == nil || r.tracker == nil {
		return nil
	}
	return r.tracker.Flush(ctx)
}

func (r *gatewayAccountingRecorder) close() error {
	if r == nil || r.tracker == nil {
		return nil
	}
	return r.tracker.Close()
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
