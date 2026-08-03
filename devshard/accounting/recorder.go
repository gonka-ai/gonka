package accounting

import (
	"context"
	"log"
	"sync"
	"time"

	"devshard/types"
)

type DiffObserverTarget interface {
	SetDiffObserver(func(types.Diff))
}

type ProtocolView interface {
	GetInference(uint64) (types.InferenceRecord, bool)
	SnapshotStateNoInferences() types.EscrowState
	Phase() types.SessionPhase
}

type RuntimeMetadata struct {
	EscrowID      string
	CreationEpoch uint64
	Model         string
	TimeoutBuffer time.Duration
}

type PhaseSource func() string

type Recorder struct {
	tracker     *Tracker
	phaseSource PhaseSource

	mu     sync.RWMutex
	states map[string]ProtocolView
}

func NewRecorder(tracker *Tracker, phaseSource PhaseSource) *Recorder {
	if tracker == nil {
		return nil
	}
	return &Recorder{
		tracker:     tracker,
		phaseSource: phaseSource,
		states:      make(map[string]ProtocolView),
	}
}

func (r *Recorder) Attach(meta RuntimeMetadata, session DiffObserverTarget, state ProtocolView) {
	if r == nil || r.tracker == nil || session == nil || state == nil {
		return
	}
	snapshot := state.SnapshotStateNoInferences()
	if err := r.tracker.RegisterEscrow(EscrowMetadata{
		EscrowID:             meta.EscrowID,
		CreationEpoch:        meta.CreationEpoch,
		Model:                meta.Model,
		Slots:                snapshot.Group,
		Phase:                escrowPhase(state.Phase()),
		RefusalTimeout:       snapshot.Config.RefusalTimeout,
		ExecutionTimeout:     snapshot.Config.ExecutionTimeout,
		TimeoutBufferSeconds: int64(meta.TimeoutBuffer / time.Second),
	}); err != nil {
		log.Printf("gateway accounting register escrow=%s: %v", meta.EscrowID, err)
		return
	}
	if err := r.tracker.SyncState(meta.EscrowID, snapshot.LatestNonce, snapshot.HostStats); err != nil {
		log.Printf("gateway accounting sync escrow=%s: %v", meta.EscrowID, err)
		return
	}
	r.mu.Lock()
	r.states[meta.EscrowID] = state
	r.mu.Unlock()
	session.SetDiffObserver(r.DiffObserver(meta.EscrowID, state))
}

func (r *Recorder) DiffObserver(escrowID string, state ProtocolView) func(types.Diff) {
	if r == nil || r.tracker == nil || state == nil {
		return func(types.Diff) {}
	}
	return func(diff types.Diff) {
		r.committedDiff(escrowID, diff, state)
	}
}

func (r *Recorder) committedDiff(escrowID string, diff types.Diff, state ProtocolView) {
	verdicts := make([]VerdictRecord, 0, len(diff.Txs))
	seen := make(map[uint64]struct{})
	for _, tx := range diff.Txs {
		var inferenceID uint64
		validation := tx.GetValidation()
		vote := tx.GetValidationVote()
		switch {
		case validation != nil:
			inferenceID = validation.InferenceId
		case vote != nil:
			inferenceID = vote.InferenceId
		default:
			continue
		}
		if _, ok := seen[inferenceID]; ok {
			continue
		}
		seen[inferenceID] = struct{}{}
		record, ok := state.GetInference(inferenceID)
		if !ok {
			continue
		}
		kind := protocolKindForStatus(record.Status)
		if kind == "" {
			continue
		}
		verdicts = append(verdicts, VerdictRecord{
			Nonce: inferenceID,
			Slot:  record.ExecutorSlot,
			Kind:  kind,
		})
	}
	if err := r.tracker.RecordCommittedDiff(escrowID, diff, verdicts); err != nil {
		log.Printf("gateway accounting diff escrow=%s nonce=%d: %v", escrowID, diff.Nonce, err)
	}
	if err := r.tracker.RecordPhase(escrowID, escrowPhase(state.Phase())); err != nil {
		log.Printf("gateway accounting phase escrow=%s: %v", escrowID, err)
	}
}

func (r *Recorder) Ghost(escrowID string, nonce uint64, reason, quarantine string) {
	if r == nil || r.tracker == nil {
		return
	}
	noSend := NoSendReasonFromString(reason)
	detail := ""
	if noSend == NoSendUnknown {
		detail = reason
	}
	if err := r.tracker.RecordGhost(
		escrowID,
		nonce,
		r.currentPhase(),
		QuarantineFromString(quarantine),
		noSend,
		detail,
	); err != nil {
		log.Printf("gateway accounting ghost escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) RealSend(escrowID string, nonce uint64, sentAt time.Time, quarantine string) {
	if r == nil || r.tracker == nil {
		return
	}
	if err := r.tracker.RecordRealSend(
		escrowID,
		nonce,
		sentAt,
		r.currentPhase(),
		QuarantineFromString(quarantine),
	); err != nil {
		log.Printf("gateway accounting real send escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) Usage(escrowID string, nonce, winnerNonce uint64) {
	if r == nil || r.tracker == nil {
		return
	}
	usage := UsageLoser
	switch {
	case winnerNonce == 0:
		usage = UsageUnknownValue
	case nonce == winnerNonce:
		usage = UsageWinner
	}
	if err := r.tracker.RecordUsage(escrowID, nonce, usage); err != nil {
		log.Printf("gateway accounting usage escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) TimeoutResult(
	escrowID string,
	nonce uint64,
	kind, action, reason, detailReason, timeoutReason string,
) {
	if r == nil || r.tracker == nil || action == "started" {
		return
	}
	if action == "skipped" && !timeoutSkipRequiresAccounting(reason) {
		return
	}
	outcome := TimeoutOutcomeFromAction(action, reason)
	if outcome == "" {
		return
	}
	if err := r.tracker.RecordTimeout(TimeoutRecord{
		EscrowID:      escrowID,
		Nonce:         nonce,
		Kind:          TimeoutKind(kind),
		Phase:         r.currentPhase(),
		Outcome:       outcome,
		Reason:        TimeoutReasonFromString(outcome, timeoutReason),
		FailureOrigin: FailureOriginFromDetail(detailReason),
		DetailReason:  detailReason,
	}); err != nil {
		log.Printf("gateway accounting timeout escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) Finalize(escrowID string) {
	r.syncAndFlush(escrowID, "", "finalize")
}

func (r *Recorder) Settled(escrowID string) {
	r.syncAndFlush(escrowID, EscrowSettled, "settle")
}

func (r *Recorder) Flush() {
	if r == nil || r.tracker == nil {
		return
	}
	if err := r.tracker.Flush(context.Background()); err != nil {
		log.Printf("gateway accounting flush: %v", err)
	}
}

func (r *Recorder) Close() error {
	if r == nil || r.tracker == nil {
		return nil
	}
	return r.tracker.Close()
}

func (r *Recorder) Tracker() *Tracker {
	if r == nil {
		return nil
	}
	return r.tracker
}

func (r *Recorder) sync(escrowID string, phase EscrowPhase) {
	if r == nil || r.tracker == nil {
		return
	}
	r.mu.RLock()
	state := r.states[escrowID]
	r.mu.RUnlock()
	if state == nil {
		return
	}
	snapshot := state.SnapshotStateNoInferences()
	if err := r.tracker.SyncState(escrowID, snapshot.LatestNonce, snapshot.HostStats); err != nil {
		log.Printf("gateway accounting sync escrow=%s: %v", escrowID, err)
	}
	if phase == "" {
		phase = escrowPhase(state.Phase())
	}
	if err := r.tracker.RecordPhase(escrowID, phase); err != nil {
		log.Printf("gateway accounting phase escrow=%s: %v", escrowID, err)
	}
}

func (r *Recorder) syncAndFlush(escrowID string, phase EscrowPhase, action string) {
	if r == nil || r.tracker == nil {
		return
	}
	r.sync(escrowID, phase)
	if err := r.tracker.Flush(context.Background()); err != nil {
		log.Printf("gateway accounting %s flush escrow=%s: %v", action, escrowID, err)
	}
}

func (r *Recorder) currentPhase() Phase {
	if r == nil || r.phaseSource == nil {
		return PhaseNormal
	}
	reason := r.phaseSource()
	switch {
	case reason == "confirmation_poc":
		return PhaseConfirmationPoC
	case reason != "":
		return PhasePoC
	default:
		return PhaseNormal
	}
}

func protocolKindForStatus(status types.InferenceStatus) ProtocolKind {
	switch status {
	case types.StatusChallenged:
		return ProtocolChallenged
	case types.StatusValidated:
		return ProtocolValidated
	case types.StatusInvalidated:
		return ProtocolInvalidated
	default:
		return ""
	}
}

func escrowPhase(phase types.SessionPhase) EscrowPhase {
	switch phase {
	case types.PhaseFinalizing:
		return EscrowFinalizing
	case types.PhaseSettlement:
		return EscrowFinalized
	default:
		return EscrowActive
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
