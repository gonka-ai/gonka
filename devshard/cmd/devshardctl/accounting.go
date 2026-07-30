package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"devshard/accounting"
	"devshard/state"
	"devshard/types"
)

type gatewayAccountingRecorder struct {
	service *accounting.Service
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newGatewayAccountingRecorder(service *accounting.Service) *gatewayAccountingRecorder {
	if service == nil || service.Book == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &gatewayAccountingRecorder{service: service, ctx: ctx, cancel: cancel}
}

func (r *gatewayAccountingRecorder) apply(event accounting.Event) {
	if r == nil || r.service == nil || r.service.Book == nil {
		return
	}
	if err := r.service.Book.Apply(event); err != nil {
		log.Printf("gateway accounting event: %v", err)
	}
}

func (r *gatewayAccountingRecorder) attachRuntime(rt *devshardRuntime) {
	if r == nil || rt == nil || rt.session == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return
	}
	r.apply(accounting.EscrowRegistered{Metadata: accounting.EscrowMetadata{
		EscrowID:      rt.id,
		CreationEpoch: rt.session.EpochID(),
		Model:         rt.model,
		Slots:         rt.session.Group(),
		Phase:         accountingEscrowPhase(rt.proxy.sm.Phase()),
	}})
	r.apply(accounting.LatestNonceObserved{EscrowID: rt.id, LatestNonce: rt.session.Nonce()})
	for slotID, stats := range rt.proxy.sm.SnapshotStateNoInferences().HostStats {
		if stats != nil {
			r.apply(accounting.HostStatsObserved{EscrowID: rt.id, SlotID: slotID, Stats: *stats})
		}
	}
	rt.session.SetDiffObserver(func(nonce uint64, hasStartInference bool) {
		r.apply(accounting.DiffApplied{EscrowID: rt.id, Nonce: nonce, Inference: hasStartInference})
	})
	rt.proxy.sm.SetTransitionObserver(func(event state.TransitionEvent) {
		r.recordProtocolTransition(rt.id, event)
	})
	if rt.proxy.redundancy != nil {
		rt.proxy.redundancy.accounting = r
	}
	rt.accountingFlush = func() {
		r.apply(accounting.EscrowPhaseChanged{
			EscrowID: rt.id,
			Phase:    accountingEscrowPhase(rt.proxy.sm.Phase()),
		})
		if err := r.flush(context.Background()); err != nil {
			log.Printf("gateway accounting flush escrow=%s: %v", rt.id, err)
		}
	}
}

func (r *gatewayAccountingRecorder) recordProtocolTransition(escrowID string, event state.TransitionEvent) {
	kind := accounting.ProtocolTransitionKind("")
	switch event.Kind {
	case state.TransitionReceipt:
		kind = accounting.ProtocolReceiptApplied
	case state.TransitionFinish:
		kind = accounting.ProtocolFinishApplied
	case state.TransitionChallenged:
		kind = accounting.ProtocolChallenged
	case state.TransitionValidated:
		kind = accounting.ProtocolValidated
	case state.TransitionInvalidated:
		kind = accounting.ProtocolInvalidated
	case state.TransitionTimeout:
		// HandleTimeout records the gateway outcome. HostStats below remains
		// the independent protocol fact used by the cross-check.
	default:
		return
	}
	if kind != "" {
		r.apply(accounting.ProtocolTransition{EscrowID: escrowID, Nonce: event.InferenceID, Kind: kind})
	}
	r.apply(accounting.HostStatsObserved{EscrowID: escrowID, SlotID: event.ExecutorSlot, Stats: event.HostStats})
}

func (r *gatewayAccountingRecorder) recordEscrowPhase(escrowID string, phase types.SessionPhase) {
	r.apply(accounting.EscrowPhaseChanged{
		EscrowID: escrowID,
		Phase:    accountingEscrowPhase(phase),
	})
}

func (r *gatewayAccountingRecorder) recordGhost(escrowID string, nonce uint64, reason, quarantine string) {
	noSend := accountingNoSendReason(reason)
	// A recognized no_send_reason already carries the whole story.
	detailReason := ""
	if noSend == accounting.NoSendUnknown {
		detailReason = reason
	}
	r.apply(accounting.Ghost{
		EscrowID:      escrowID,
		Nonce:         nonce,
		DispatchPhase: currentAccountingPhase(),
		Quarantine:    accountingQuarantine(quarantine),
		Reason:        noSend,
		DetailReason:  detailReason,
	})
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

func (r *gatewayAccountingRecorder) recordRealSend(escrowID string, nonce uint64, quarantine string) {
	r.apply(accounting.RealSend{
		EscrowID:      escrowID,
		Nonce:         nonce,
		DispatchPhase: currentAccountingPhase(),
		Quarantine:    accountingQuarantine(quarantine),
	})
}

func (r *gatewayAccountingRecorder) recordUsage(escrowID string, nonce, winnerNonce uint64) {
	switch {
	case winnerNonce == 0:
		r.apply(accounting.UsageUnknown{EscrowID: escrowID, Nonce: nonce})
	case nonce == winnerNonce:
		r.apply(accounting.Winner{EscrowID: escrowID, Nonce: nonce})
	default:
		r.apply(accounting.Loser{EscrowID: escrowID, Nonce: nonce})
	}
}

func (r *gatewayAccountingRecorder) recordTimeout(
	escrowID string,
	nonce uint64,
	kind, action, reason, detailReason, timeoutReason string,
) {
	if action == "started" || action == "completed" || action == "failed" ||
		(action == "skipped" && timeoutSkipRequiresAccounting(reason)) {
		r.apply(accounting.TimeoutRequired{
			EscrowID:        escrowID,
			Nonce:           nonce,
			Kind:            accounting.TimeoutKind(kind),
			EvaluationPhase: currentAccountingPhase(),
			FailureOrigin:   accountingFailureOrigin(detailReason),
			DetailReason:    detailReason,
		})
	}
	var outcome accounting.TimeoutOutcome
	switch {
	case action == "completed":
		outcome = accounting.TimeoutApplied
	case action == "skipped" && timeoutSkipRequiresAccounting(reason):
		outcome = accounting.TimeoutSkipped
	case action == "failed":
		outcome = accounting.TimeoutOutcome(reason)
	}
	if outcome == "" {
		return
	}
	r.apply(accounting.TimeoutOutcomeRecorded{
		EscrowID: escrowID,
		Nonce:    nonce,
		Outcome:  outcome,
		Reason:   accountingTimeoutReason(outcome, timeoutReason),
	})
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
	r.cancel()
	r.wg.Wait()
	return r.service.Close()
}

func (r *gatewayAccountingRecorder) schedule(deadline time.Time, record func()) {
	if r == nil || record == nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		timer := time.NewTimer(max(time.Until(deadline), 0))
		defer timer.Stop()
		select {
		case <-timer.C:
			record()
		case <-r.ctx.Done():
		}
	}()
}

func (r *gatewayAccountingRecorder) handler(current accounting.CurrentEpochFunc) http.Handler {
	if r == nil || r.service == nil {
		return http.NotFoundHandler()
	}
	return accounting.NewHandler(r.service.Book, current)
}

func accountingEscrowPhase(phase types.SessionPhase) accounting.EscrowPhase {
	switch phase {
	case types.PhaseFinalizing:
		return accounting.EscrowFinalizing
	case types.PhaseSettlement:
		return accounting.EscrowSettled
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

func accountingRetentionEpochs() uint64 {
	value := readInt64Env("DEVSHARD_STATS_RETENTION_EPOCHS", 0)
	if value < 0 {
		log.Printf("invalid DEVSHARD_STATS_RETENTION_EPOCHS=%d, using 0", value)
		return 0
	}
	return uint64(value)
}

func accountingCurrentEpoch(g *Gateway) accounting.CurrentEpochFunc {
	return func(context.Context) (uint64, error) {
		if g == nil || g.phaseGate == nil {
			return 0, fmt.Errorf("chain phase is unavailable")
		}
		epoch := g.phaseGate.Snapshot().EpochIndex
		if epoch == 0 {
			return 0, fmt.Errorf("current epoch is unavailable")
		}
		return epoch, nil
	}
}

func startAccountingServer(g *Gateway, address string) *http.Server {
	if g == nil || g.accounting == nil || strings.TrimSpace(address) == "" {
		return nil
	}
	server := &http.Server{
		Addr:    address,
		Handler: g.accounting.handler(accountingCurrentEpoch(g)),
	}
	go func() {
		log.Printf("devshard accounting API listening on %s", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("devshard accounting API stopped: %v", err)
		}
	}()
	return server
}
