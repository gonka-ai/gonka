package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"devshard/accounting"
	"devshard/types"
	"devshard/user"
)

type gatewayAccountingRecorder struct {
	service *accounting.Service
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu       sync.RWMutex
	runtimes map[string]*accountingRuntime
}

type accountingRuntime struct {
	rt        *devshardRuntime
	mu        sync.Mutex
	lastNonce uint64
}

func newGatewayAccountingRecorder(service *accounting.Service) *gatewayAccountingRecorder {
	if service == nil || service.Book == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &gatewayAccountingRecorder{
		service:  service,
		ctx:      ctx,
		cancel:   cancel,
		runtimes: make(map[string]*accountingRuntime),
	}
	if service.Store != nil {
		recorder.wg.Add(1)
		go recorder.reconcileLoop()
	}
	return recorder
}

func (r *gatewayAccountingRecorder) apply(event accounting.Event) {
	if r == nil || r.service == nil || r.service.Book == nil {
		return
	}
	if err := r.service.Book.Apply(event); err != nil {
		log.Printf("gateway accounting event: %v", err)
	}
}

func (r *gatewayAccountingRecorder) applyBatch(events []accounting.Event) bool {
	if r == nil || r.service == nil || r.service.Book == nil {
		return false
	}
	if err := r.service.Book.ApplyBatch(events); err != nil {
		log.Printf("gateway accounting batch: %v", err)
		return false
	}
	return true
}

func (r *gatewayAccountingRecorder) attachRuntime(rt *devshardRuntime) {
	if r == nil || rt == nil || rt.session == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return
	}
	stateSnapshot := rt.proxy.sm.SnapshotStateNoInferences()
	r.apply(accounting.EscrowRegistered{Metadata: accounting.EscrowMetadata{
		EscrowID:      rt.id,
		CreationEpoch: rt.session.EpochID(),
		Model:         rt.model,
		Slots:         stateSnapshot.Group,
		Phase:         accountingEscrowPhase(rt.proxy.sm.Phase()),
	}})
	tracked := &accountingRuntime{
		rt:        rt,
		lastNonce: r.service.Book.LatestNonce(rt.id),
	}
	r.mu.Lock()
	r.runtimes[rt.id] = tracked
	r.mu.Unlock()
	r.reconcileRuntime(tracked)
	if rt.proxy.redundancy != nil {
		rt.proxy.redundancy.accounting = r
	}
	rt.accountingFlush = func() {
		r.reconcileRuntime(tracked)
		r.apply(accounting.EscrowPhaseChanged{
			EscrowID: rt.id,
			Phase:    accountingEscrowPhase(rt.proxy.sm.Phase()),
		})
		if err := r.flush(context.Background()); err != nil {
			log.Printf("gateway accounting flush escrow=%s: %v", rt.id, err)
		}
		r.detachRuntime(rt.id, tracked)
	}
}

func (r *gatewayAccountingRecorder) reconcileLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.reconcileAll()
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *gatewayAccountingRecorder) reconcileAll() {
	r.mu.RLock()
	runtimes := make([]*accountingRuntime, 0, len(r.runtimes))
	for _, tracked := range r.runtimes {
		runtimes = append(runtimes, tracked)
	}
	r.mu.RUnlock()
	for _, tracked := range runtimes {
		r.reconcileRuntime(tracked)
	}
}

func (r *gatewayAccountingRecorder) detachRuntime(escrowID string, tracked *accountingRuntime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimes[escrowID] == tracked {
		delete(r.runtimes, escrowID)
	}
}

func (r *gatewayAccountingRecorder) runtime(escrowID string) *accountingRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.runtimes[escrowID]
}

func (r *gatewayAccountingRecorder) reconcileEscrow(escrowID string) {
	if tracked := r.runtime(escrowID); tracked != nil {
		r.reconcileRuntime(tracked)
	}
}

func (r *gatewayAccountingRecorder) reconcileRuntime(tracked *accountingRuntime) {
	if tracked == nil || tracked.rt == nil || tracked.rt.session == nil ||
		tracked.rt.proxy == nil || tracked.rt.proxy.sm == nil {
		return
	}
	tracked.mu.Lock()
	defer tracked.mu.Unlock()

	rt := tracked.rt
	for _, diff := range rt.session.DiffsAfter(tracked.lastNonce) {
		if !r.recordDiff(rt, diff) {
			break
		}
		tracked.lastNonce = diff.Nonce
	}
	r.reconcilePending(rt)
	for slotID, stats := range rt.proxy.sm.SnapshotStateNoInferences().HostStats {
		if stats != nil {
			r.apply(accounting.HostStatsObserved{EscrowID: rt.id, SlotID: slotID, Stats: *stats})
		}
	}
}

func (r *gatewayAccountingRecorder) recordDiff(rt *devshardRuntime, diff types.Diff) bool {
	hasStart := false
	events := make([]accounting.Event, 0, len(diff.Txs)+2)
	for _, tx := range diff.Txs {
		if tx.GetStartInference() != nil {
			hasStart = true
			break
		}
	}
	for _, tx := range diff.Txs {
		switch {
		case tx.GetConfirmStart() != nil:
			events = append(events, accounting.ProtocolTransition{
				EscrowID: rt.id,
				Nonce:    tx.GetConfirmStart().InferenceId,
				Kind:     accounting.ProtocolReceiptApplied,
			})
		case tx.GetFinishInference() != nil:
			events = append(events, accounting.ProtocolTransition{
				EscrowID: rt.id,
				Nonce:    tx.GetFinishInference().InferenceId,
				Kind:     accounting.ProtocolFinishApplied,
			})
		case tx.GetValidation() != nil:
			if event := r.validationStatusEvent(rt, tx.GetValidation().InferenceId); event != nil {
				events = append(events, event)
			}
		case tx.GetValidationVote() != nil:
			if event := r.validationStatusEvent(rt, tx.GetValidationVote().InferenceId); event != nil {
				events = append(events, event)
			}
		}
	}
	for slotID, stats := range rt.proxy.sm.SnapshotStateNoInferences().HostStats {
		if stats != nil {
			events = append(events, accounting.HostStatsObserved{
				EscrowID: rt.id, SlotID: slotID, Stats: *stats,
			})
		}
	}
	events = append(events, accounting.DiffApplied{
		EscrowID: rt.id, Nonce: diff.Nonce, Inference: hasStart,
	})
	return r.applyBatch(events)
}

func (r *gatewayAccountingRecorder) validationStatusEvent(rt *devshardRuntime, nonce uint64) accounting.Event {
	record, found := rt.proxy.sm.GetCommittedRecord(nonce)
	if !found {
		return nil
	}
	var kind accounting.ProtocolTransitionKind
	switch record.Status {
	case types.StatusChallenged:
		kind = accounting.ProtocolChallenged
	case types.StatusValidated:
		kind = accounting.ProtocolValidated
	case types.StatusInvalidated:
		kind = accounting.ProtocolInvalidated
	default:
		return nil
	}
	return accounting.ProtocolTransition{EscrowID: rt.id, Nonce: nonce, Kind: kind}
}

func (r *gatewayAccountingRecorder) reconcilePending(rt *devshardRuntime) {
	for _, pending := range r.service.Book.PendingTimeouts(rt.id) {
		record, found := rt.proxy.sm.GetCommittedRecord(pending.Nonce)
		if !found {
			continue
		}
		switch record.Status {
		case types.StatusTimedOut:
			r.apply(accounting.TimeoutOutcomeRecorded{
				EscrowID: rt.id,
				Nonce:    pending.Nonce,
				Outcome:  accounting.TimeoutApplied,
			})
		case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
			if pending.Usage == "" {
				r.apply(accounting.UsageUnknown{EscrowID: rt.id, Nonce: pending.Nonce})
			}
			r.apply(accounting.ProtocolTransition{
				EscrowID: rt.id,
				Nonce:    pending.Nonce,
				Kind:     accounting.ProtocolFinishApplied,
			})
			if record.Status == types.StatusInvalidated {
				r.apply(accounting.ProtocolTransition{
					EscrowID: rt.id,
					Nonce:    pending.Nonce,
					Kind:     accounting.ProtocolInvalidated,
				})
			}
		}
	}
}

func (r *gatewayAccountingRecorder) recordEscrowPhase(escrowID string, phase types.SessionPhase) {
	r.apply(accounting.EscrowPhaseChanged{
		EscrowID: escrowID,
		Phase:    accountingEscrowPhase(phase),
	})
}

func (r *gatewayAccountingRecorder) recordSettled(escrowID string) {
	r.apply(accounting.EscrowPhaseChanged{
		EscrowID: escrowID,
		Phase:    accounting.EscrowSettled,
	})
}

func (r *gatewayAccountingRecorder) recordGhost(escrowID string, nonce uint64, reason, quarantine string) {
	r.reconcileEscrow(escrowID)
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

func (r *gatewayAccountingRecorder) recordRealSend(
	escrowID string,
	nonce uint64,
	quarantine string,
	session *user.Session,
	sendTime time.Time,
) {
	r.reconcileEscrow(escrowID)
	r.apply(accounting.RealSend{
		EscrowID:      escrowID,
		Nonce:         nonce,
		DispatchPhase: currentAccountingPhase(),
		Quarantine:    accountingQuarantine(quarantine),
	})
	r.scheduleTimeoutEligibility(escrowID, nonce, session, sendTime)
}

func (r *gatewayAccountingRecorder) scheduleTimeoutEligibility(
	escrowID string,
	nonce uint64,
	session *user.Session,
	sendTime time.Time,
) {
	if session == nil {
		return
	}
	_, deadline := session.TimeoutDeadline(nonce, sendTime)
	r.schedule(deadline, func() {
		tracked := r.runtime(escrowID)
		if tracked == nil || tracked.rt == nil || tracked.rt.proxy == nil ||
			tracked.rt.proxy.sm == nil {
			return
		}
		if record, found := tracked.rt.proxy.sm.GetCommittedRecord(nonce); found {
			switch record.Status {
			case types.StatusFinished, types.StatusChallenged, types.StatusValidated,
				types.StatusInvalidated, types.StatusTimedOut:
				return
			}
		}
		kind, currentDeadline := session.TimeoutDeadline(nonce, sendTime)
		if currentDeadline.After(time.Now()) {
			r.scheduleTimeoutEligibility(escrowID, nonce, session, sendTime)
			return
		}
		r.apply(accounting.TimeoutRequired{
			EscrowID:        escrowID,
			Nonce:           nonce,
			Kind:            accounting.TimeoutKind(kind),
			EvaluationPhase: currentAccountingPhase(),
			FailureOrigin:   accounting.FailureTransportUnknown,
		})
	})
}

func (r *gatewayAccountingRecorder) recordUsage(escrowID string, nonce, winnerNonce uint64) {
	r.reconcileEscrow(escrowID)
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
	session *user.Session,
	sendTime time.Time,
) {
	r.reconcileEscrow(escrowID)
	if action == "started" {
		if session == nil {
			r.recordTimeoutNow(
				escrowID, nonce, accounting.TimeoutKind(kind), action,
				reason, detailReason, timeoutReason,
			)
		}
		return
	}
	if action == "skipped" {
		if timeoutSkipRequiresAccounting(reason) {
			r.recordTimeoutAtDeadline(
				escrowID, nonce, accounting.TimeoutKind(kind), reason,
				detailReason, timeoutReason, session, sendTime,
			)
		}
		return
	}
	r.recordTimeoutNow(
		escrowID, nonce, accounting.TimeoutKind(kind), action, reason, detailReason, timeoutReason,
	)
}

func (r *gatewayAccountingRecorder) recordTimeoutAtDeadline(
	escrowID string,
	nonce uint64,
	fallbackKind accounting.TimeoutKind,
	reason, detailReason, timeoutReason string,
	session *user.Session,
	sendTime time.Time,
) {
	if session == nil {
		r.recordTimeoutNow(
			escrowID, nonce, fallbackKind, "skipped",
			reason, detailReason, timeoutReason,
		)
		return
	}
	kind, deadline := session.TimeoutDeadline(nonce, sendTime)
	if deadline.After(time.Now()) {
		r.schedule(deadline, func() {
			r.recordTimeoutAtDeadline(
				escrowID, nonce, fallbackKind, reason,
				detailReason, timeoutReason, session, sendTime,
			)
		})
		return
	}
	if tracked := r.runtime(escrowID); tracked != nil && tracked.rt != nil &&
		tracked.rt.proxy != nil && tracked.rt.proxy.sm != nil {
		if record, found := tracked.rt.proxy.sm.GetCommittedRecord(nonce); found {
			switch record.Status {
			case types.StatusFinished, types.StatusChallenged, types.StatusValidated,
				types.StatusInvalidated, types.StatusTimedOut:
				return
			}
		}
	}
	r.recordTimeoutNow(
		escrowID, nonce, accounting.TimeoutKind(kind), "skipped",
		reason, detailReason, timeoutReason,
	)
}

func (r *gatewayAccountingRecorder) recordTimeoutNow(
	escrowID string,
	nonce uint64,
	kind accounting.TimeoutKind,
	action, reason, detailReason, timeoutReason string,
) {
	if action == "started" || action == "completed" || action == "failed" || action == "skipped" {
		r.apply(accounting.TimeoutRequired{
			EscrowID:        escrowID,
			Nonce:           nonce,
			Kind:            kind,
			EvaluationPhase: currentAccountingPhase(),
			FailureOrigin:   accountingFailureOrigin(detailReason),
			DetailReason:    detailReason,
		})
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

func startAccountingServer(g *Gateway, address string) (*http.Server, error) {
	if g == nil || g.accounting == nil || strings.TrimSpace(address) == "" {
		return nil, nil
	}
	server := &http.Server{
		Addr:              address,
		Handler:           g.accounting.handler(accountingCurrentEpoch(g)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on accounting address %s: %w", address, err)
	}
	go func() {
		log.Printf("devshard accounting API listening on %s", address)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("devshard accounting API stopped: %v", err)
		}
	}()
	return server, nil
}
