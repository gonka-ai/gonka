package accounting

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"devshard/types"
)

const DefaultSnapshotInterval = 5 * time.Minute

type timeoutConfig struct {
	refusal   time.Duration
	execution time.Duration
	buffer    time.Duration
}

type Service struct {
	Book  *Book
	Store *Store

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	recordMu  sync.Mutex
	timeouts  map[string]timeoutConfig
	deadlines *deadlineScheduler
	phase     func() Phase
}

func OpenService(path string, retentionEpochs uint64, interval time.Duration) (*Service, error) {
	store, err := OpenStore(path, retentionEpochs)
	if err != nil {
		return nil, err
	}
	book, err := store.Load(context.Background())
	if err != nil {
		store.Close()
		return nil, err
	}
	if interval <= 0 {
		interval = DefaultSnapshotInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		Book:     book,
		Store:    store,
		cancel:   cancel,
		done:     make(chan struct{}),
		timeouts: make(map[string]timeoutConfig),
	}
	service.deadlines = newDeadlineScheduler(service.onDeadline)
	service.deadlines.start()
	go service.run(ctx, interval)
	return service, nil
}

// Tracker is the lifecycle-owning implementation of Recorder.
type Tracker = Service

func OpenTracker(path string, retentionEpochs uint64, interval time.Duration) (*Tracker, error) {
	return OpenService(path, retentionEpochs, interval)
}

func (s *Service) SetPhaseProvider(provider func() Phase) {
	if s != nil {
		s.phase = provider
	}
}

func (s *Service) run(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.Flush(ctx); err != nil {
				log.Printf("accounting snapshot: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) Flush(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	return s.Store.Snapshot(ctx, s.Book)
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var result error
	s.once.Do(func() {
		s.cancel()
		<-s.done
		s.deadlines.close()
		if err := s.Flush(context.Background()); err != nil {
			result = err
		}
		if err := s.Store.Close(); err != nil && result == nil {
			result = err
		}
	})
	return result
}

func (s *Service) RegisterEscrow(
	metadata EscrowMetadata,
	config types.SessionConfig,
	timeoutBuffer ...time.Duration,
) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	buffer := time.Duration(0)
	if len(timeoutBuffer) > 0 {
		buffer = timeoutBuffer[0]
	}
	if buffer < 0 {
		return errors.New("accounting timeout buffer cannot be negative")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if err := s.Book.Apply(EscrowRegistered{Metadata: metadata}); err != nil {
		return err
	}
	s.timeouts[metadata.EscrowID] = timeoutConfig{
		refusal:   time.Duration(config.RefusalTimeout) * time.Second,
		execution: time.Duration(config.ExecutionTimeout) * time.Second,
		buffer:    buffer,
	}
	return nil
}

func (s *Service) RecordCommittedDiff(escrowID string, diff types.Diff, view ProtocolView) error {
	return s.recordCommittedDiff(escrowID, diff, view, true)
}

func (s *Service) RecordRecoveredDiff(escrowID string, diff types.Diff, view ProtocolView) error {
	return s.recordCommittedDiff(escrowID, diff, view, false)
}

func (s *Service) recordCommittedDiff(
	escrowID string,
	diff types.Diff,
	view ProtocolView,
	recordPhase bool,
) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	reducedThrough := s.Book.ReducedThrough(escrowID)
	if diff.Nonce <= reducedThrough {
		return nil
	}
	if diff.Nonce != reducedThrough+1 {
		return fmt.Errorf(
			"accounting committed diff gap for escrow %s: expected %d, got %d",
			escrowID,
			reducedThrough+1,
			diff.Nonce,
		)
	}

	inference := false
	for _, tx := range diff.Txs {
		if start := tx.GetStartInference(); start != nil && start.InferenceId == diff.Nonce {
			inference = true
			break
		}
	}
	events := make([]Event, 0, len(diff.Txs)+4)
	validationIDs := make(map[uint64]struct{})
	receiptTimes := make(map[uint64]int64)
	cancelDeadlines := make(map[uint64]struct{})
	terminalPhase := false
	for _, tx := range diff.Txs {
		if tx == nil {
			continue
		}
		if confirm := tx.GetConfirmStart(); confirm != nil {
			events = append(events, ProtocolTransition{
				EscrowID: escrowID, Nonce: confirm.InferenceId, Kind: ProtocolReceiptApplied,
			})
			receiptTimes[confirm.InferenceId] = confirm.ConfirmedAt
		}
		if finish := tx.GetFinishInference(); finish != nil {
			events = append(events, ProtocolTransition{
				EscrowID: escrowID, Nonce: finish.InferenceId, Kind: ProtocolFinishApplied,
			})
			cancelDeadlines[finish.InferenceId] = struct{}{}
		}
		if timeout := tx.GetTimeoutInference(); timeout != nil {
			_, sent := s.Book.sendState(escrowID, timeout.InferenceId)
			if !sent {
				cancelDeadlines[timeout.InferenceId] = struct{}{}
				continue
			}
			kind := TimeoutExecution
			if timeout.Reason == types.TimeoutReason_TIMEOUT_REASON_REFUSED {
				kind = TimeoutRefused
			}
			phase := s.dispatchPhase(escrowID, timeout.InferenceId)
			events = append(events, TimeoutRequired{
				EscrowID: escrowID, Nonce: timeout.InferenceId, Kind: kind,
				EvaluationPhase: phase, FailureOrigin: FailureHostResponse,
			}, TimeoutOutcomeRecorded{
				EscrowID: escrowID, Nonce: timeout.InferenceId, Outcome: TimeoutApplied,
			})
			cancelDeadlines[timeout.InferenceId] = struct{}{}
		}
		if validation := tx.GetValidation(); validation != nil {
			validationIDs[validation.InferenceId] = struct{}{}
		}
		if vote := tx.GetValidationVote(); vote != nil {
			validationIDs[vote.InferenceId] = struct{}{}
		}
	}
	if view != nil {
		for nonce := range validationIDs {
			if record, ok := view.GetCommittedRecord(nonce); ok {
				events = append(events, validationStatusEvents(escrowID, nonce, record.Status)...)
			}
		}
		snapshot := view.SnapshotStateNoInferences()
		for slotID, stats := range snapshot.HostStats {
			if stats == nil {
				continue
			}
			events = append(events, HostStatsObserved{
				EscrowID: escrowID, SlotID: slotID, Stats: *stats,
			})
		}
		if recordPhase {
			switch snapshot.Phase {
			case types.PhaseFinalizing:
				events = append(events, EscrowPhaseChanged{EscrowID: escrowID, Phase: EscrowFinalizing})
			case types.PhaseSettlement:
				events = append(events, EscrowPhaseChanged{EscrowID: escrowID, Phase: EscrowFinalized})
				terminalPhase = true
			}
		}
	}
	events = append(events, DiffApplied{
		EscrowID: escrowID, Nonce: diff.Nonce, Inference: inference,
	})
	if err := s.Book.ApplyBatch(events); err != nil {
		return err
	}
	if terminalPhase {
		s.deadlines.cancelEscrow(escrowID)
		return nil
	}
	for nonce, confirmedAt := range receiptTimes {
		if config, ok := s.timeouts[escrowID]; ok {
			s.deadlines.upsert(
				escrowID,
				nonce,
				time.Unix(confirmedAt, 0).Add(config.execution+config.buffer),
				TimeoutExecution,
				s.dispatchPhase(escrowID, nonce),
			)
		}
	}
	for nonce := range cancelDeadlines {
		s.deadlines.cancel(escrowID, nonce)
	}
	return nil
}

func (s *Service) ReconcilePending(escrowID string, view ProtocolView) error {
	if s == nil || s.Book == nil || view == nil {
		return nil
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	var events []Event
	var cancel []uint64
	for _, pending := range s.Book.PendingTimeouts(escrowID) {
		record, found := view.GetCommittedRecord(pending.Nonce)
		if !found {
			continue
		}
		switch record.Status {
		case types.StatusTimedOut:
			events = append(events, TimeoutOutcomeRecorded{
				EscrowID: escrowID, Nonce: pending.Nonce, Outcome: TimeoutApplied,
			})
			cancel = append(cancel, pending.Nonce)
		case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
			if pending.Usage == "" {
				events = append(events, UsageUnknown{EscrowID: escrowID, Nonce: pending.Nonce})
			}
			events = append(events, ProtocolTransition{
				EscrowID: escrowID, Nonce: pending.Nonce, Kind: ProtocolFinishApplied,
			})
			events = append(events, validationStatusEvents(escrowID, pending.Nonce, record.Status)...)
			cancel = append(cancel, pending.Nonce)
		}
	}
	if err := s.Book.ApplyBatch(events); err != nil {
		return err
	}
	for _, nonce := range cancel {
		s.deadlines.cancel(escrowID, nonce)
	}
	return nil
}

func (s *Service) RecordRealSend(
	escrowID string,
	nonce uint64,
	phase Phase,
	quarantine QuarantineMode,
	sentAt ...time.Time,
) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	config, ok := s.timeouts[escrowID]
	if !ok {
		return errors.New("escrow timeout configuration is unavailable")
	}
	_, alreadySent := s.Book.sendState(escrowID, nonce)
	if err := s.Book.Apply(RealSend{
		EscrowID: escrowID, Nonce: nonce, DispatchPhase: phase, Quarantine: quarantine,
	}); err != nil {
		return err
	}
	exists, sent := s.Book.sendState(escrowID, nonce)
	if alreadySent || !exists || !sent {
		return nil
	}
	sendTime := s.deadlines.now()
	if len(sentAt) > 0 && !sentAt[0].IsZero() {
		sendTime = sentAt[0]
	}
	s.deadlines.upsert(
		escrowID,
		nonce,
		sendTime.Add(config.refusal+config.buffer),
		TimeoutRefused,
		phase,
	)
	return nil
}

func (s *Service) RecordGhost(
	escrowID string,
	nonce uint64,
	phase Phase,
	quarantine QuarantineMode,
	reason NoSendReason,
	detailReason string,
) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	return s.Book.Apply(Ghost{
		EscrowID: escrowID, Nonce: nonce, DispatchPhase: phase,
		Quarantine: quarantine, Reason: reason, DetailReason: detailReason,
	})
}

func (s *Service) RecordUsage(escrowID string, nonce uint64, usage Usage) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	var event Event
	switch usage {
	case UsageWinner:
		event = Winner{EscrowID: escrowID, Nonce: nonce}
	case UsageLoser:
		event = Loser{EscrowID: escrowID, Nonce: nonce}
	case UsageUnknownValue:
		event = UsageUnknown{EscrowID: escrowID, Nonce: nonce}
	default:
		return errors.New("invalid accounting usage")
	}
	return s.Book.Apply(event)
}

func (s *Service) RecordTimeoutOutcome(record TimeoutRecord) error {
	if s == nil || s.Book == nil || s.deadlines == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if record.Outcome == TimeoutSkipped && s.deadlines.deferOutcome(record) {
		return nil
	}
	if err := s.Book.Apply(TimeoutRequired{
		EscrowID:        record.EscrowID,
		Nonce:           record.Nonce,
		Kind:            record.Kind,
		EvaluationPhase: record.Phase,
		FailureOrigin:   record.FailureOrigin,
		DetailReason:    record.DetailReason,
	}); err != nil {
		return err
	}
	if err := s.Book.Apply(TimeoutOutcomeRecorded{
		EscrowID: record.EscrowID,
		Nonce:    record.Nonce,
		Outcome:  record.Outcome,
		Reason:   record.Reason,
	}); err != nil {
		return err
	}
	s.deadlines.cancel(record.EscrowID, record.Nonce)
	return nil
}

func (s *Service) RecordPhase(escrowID string, phase EscrowPhase) error {
	if s == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if phase == EscrowFinalized || phase == EscrowSettled {
		s.deadlines.cancelEscrow(escrowID)
	}
	return s.Book.Apply(EscrowPhaseChanged{EscrowID: escrowID, Phase: phase})
}

func (s *Service) Diagnostics() []DiagnosticEntry {
	if s == nil || s.Book == nil {
		return nil
	}
	return s.Book.Diagnostics()
}

func validationStatusEvents(
	escrowID string,
	nonce uint64,
	status types.InferenceStatus,
) []Event {
	switch status {
	case types.StatusChallenged:
		return []Event{ProtocolTransition{EscrowID: escrowID, Nonce: nonce, Kind: ProtocolChallenged}}
	case types.StatusValidated:
		return []Event{ProtocolTransition{EscrowID: escrowID, Nonce: nonce, Kind: ProtocolValidated}}
	case types.StatusInvalidated:
		return []Event{
			ProtocolTransition{EscrowID: escrowID, Nonce: nonce, Kind: ProtocolChallenged},
			ProtocolTransition{EscrowID: escrowID, Nonce: nonce, Kind: ProtocolInvalidated},
		}
	default:
		return nil
	}
}

func (s *Service) dispatchPhase(escrowID string, nonce uint64) Phase {
	s.Book.mu.RLock()
	defer s.Book.mu.RUnlock()
	if escrow := s.Book.escrows[escrowID]; escrow != nil {
		if state := escrow.live[nonce]; state != nil && state.dispatchPhase != "" {
			return state.dispatchPhase
		}
	}
	return PhaseNormal
}

func (s *Service) onDeadline(entry *deadlineEntry) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if !s.deadlines.claim(entry) {
		return
	}
	detail := "not_finished"
	if entry.kind == TimeoutRefused {
		detail = "no_receipt"
	}
	if err := s.Book.Apply(TimeoutRequired{
		EscrowID:        entry.key.escrowID,
		Nonce:           entry.key.nonce,
		Kind:            entry.kind,
		EvaluationPhase: s.currentPhase(entry.phase),
		FailureOrigin:   FailureTransportUnknown,
		DetailReason:    detail,
	}); err != nil {
		log.Printf("accounting deadline: %v", err)
		return
	}
	if entry.outcome != nil {
		entry.outcome.Phase = s.currentPhase(entry.phase)
		if err := s.Book.Apply(TimeoutRequired{
			EscrowID:        entry.key.escrowID,
			Nonce:           entry.key.nonce,
			Kind:            entry.outcome.Kind,
			EvaluationPhase: entry.outcome.Phase,
			FailureOrigin:   entry.outcome.FailureOrigin,
			DetailReason:    entry.outcome.DetailReason,
		}); err != nil {
			log.Printf("accounting deferred timeout details: %v", err)
			return
		}
		if err := s.Book.Apply(TimeoutOutcomeRecorded{
			EscrowID: entry.outcome.EscrowID,
			Nonce:    entry.outcome.Nonce,
			Outcome:  entry.outcome.Outcome,
			Reason:   entry.outcome.Reason,
		}); err != nil {
			log.Printf("accounting deferred timeout outcome: %v", err)
		}
	}
}

func (s *Service) currentPhase(fallback Phase) Phase {
	if s.phase != nil {
		return s.phase()
	}
	return fallback
}

var _ Recorder = (*Service)(nil)
