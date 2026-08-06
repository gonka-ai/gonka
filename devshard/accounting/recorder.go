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

// ProtocolView is the protocol state accounting reads. The per-diff path uses
// only Phase and HostStatsFor; the snapshot is for attach and lifecycle sync.
type ProtocolView interface {
	GetInference(uint64) (types.InferenceRecord, bool)
	Phase() types.SessionPhase
	HostStatsFor(slot uint32) (types.HostStats, bool)
	SnapshotStateNoInferences() types.EscrowState
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
		Phase:                escrowPhase(snapshot.Phase),
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

// DiffObserver returns the callback the session invokes after a diff commits.
//
// TODO: move the call outside Session.mu. Holding it is what orders a
// start-inference diff before the Ghost and RealSend facts for that nonce, and
// a fact naming an unregistered nonce is dropped.
func (r *Recorder) DiffObserver(escrowID string, state ProtocolView) func(types.Diff) {
	if r == nil || r.tracker == nil || state == nil {
		return func(types.Diff) {}
	}
	return func(diff types.Diff) {
		r.committedDiff(escrowID, diff, state)
	}
}

// committedDiff runs on the sequencer's critical section, so it reads only what
// the diff cannot tell it and allocates nothing for a plain start inference.
func (r *Recorder) committedDiff(escrowID string, diff types.Diff, state ProtocolView) {
	var verdicts []VerdictRecord
	var seen map[uint64]struct{}
	var hostStats map[uint32]*types.HostStats

	for _, tx := range diff.Txs {
		// An applied timeout moves HostStats.Missed and a verdict moves
		// HostStats.Invalid, both on the executor slot. Nothing else in a diff
		// touches the tallies.
		var inferenceID uint64
		verdict := false
		switch timeout, validation, vote := tx.GetTimeoutInference(), tx.GetValidation(), tx.GetValidationVote(); {
		case timeout != nil:
			inferenceID = timeout.InferenceId
		case validation != nil:
			inferenceID, verdict = validation.InferenceId, true
		case vote != nil:
			inferenceID, verdict = vote.InferenceId, true
		default:
			continue
		}
		if _, ok := seen[inferenceID]; ok {
			continue
		}
		if seen == nil {
			seen = make(map[uint64]struct{})
		}
		seen[inferenceID] = struct{}{}
		record, ok := state.GetInference(inferenceID)
		if !ok {
			continue
		}
		hostStats = r.appendHostStats(record.ExecutorSlot, state, hostStats)
		if !verdict {
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

	phase := escrowPhase(state.Phase())
	if err := r.tracker.RecordCommittedState(escrowID, diff, verdicts, phase, hostStats); err != nil {
		log.Printf("gateway accounting diff escrow=%s nonce=%d: %v", escrowID, diff.Nonce, err)
	}
}

func (r *Recorder) appendHostStats(
	slot uint32,
	state ProtocolView,
	into map[uint32]*types.HostStats,
) map[uint32]*types.HostStats {
	if _, done := into[slot]; done {
		return into
	}
	stats, ok := state.HostStatsFor(slot)
	if !ok {
		return into
	}
	if into == nil {
		into = make(map[uint32]*types.HostStats, 1)
	}
	into[slot] = &stats
	return into
}

// Ghost records a burned nonce and returns the dispatch phase it stamped, so
// the caller can label the attempt span with the same value the counter got.
func (r *Recorder) Ghost(ctx context.Context, escrowID string, nonce uint64, reason, quarantine string) Phase {
	phase := r.currentPhase()
	if r == nil || r.tracker == nil {
		return phase
	}
	noSend, detail := NoSendFromReason(reason)
	if err := r.tracker.RecordGhost(
		escrowID,
		nonce,
		phase,
		QuarantineFromString(quarantine),
		noSend,
		detail,
		TraceRefFromContext(ctx),
	); err != nil {
		log.Printf("gateway accounting ghost escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
	return phase
}

// RealSend records the dispatch and returns the phase it stamped. That phase,
// not the phase at some later instant, is the one the nonce keeps for the rest
// of its life, so terminal-time callers must reuse this value.
func (r *Recorder) RealSend(ctx context.Context, escrowID string, nonce uint64, sentAt time.Time, quarantine string) Phase {
	phase := r.currentPhase()
	if r == nil || r.tracker == nil {
		return phase
	}
	if err := r.tracker.RecordRealSend(
		escrowID,
		nonce,
		sentAt,
		phase,
		QuarantineFromString(quarantine),
		TraceRefFromContext(ctx),
	); err != nil {
		log.Printf("gateway accounting real send escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
	return phase
}

func (r *Recorder) Usage(ctx context.Context, escrowID string, nonce, winnerNonce uint64) {
	if r == nil || r.tracker == nil {
		return
	}
	if err := r.tracker.RecordUsage(escrowID, nonce, UsageFor(nonce, winnerNonce), TraceRefFromContext(ctx)); err != nil {
		log.Printf("gateway accounting usage escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

// TimeoutResult records a timeout outcome and returns the evaluation phase it
// stamped. Actions that TimeoutActionRecorded rejects are not recorded.
func (r *Recorder) TimeoutResult(
	ctx context.Context,
	escrowID string,
	nonce uint64,
	kind, action, reason, detailReason, timeoutReason string,
) Phase {
	phase := r.currentPhase()
	if r == nil || r.tracker == nil || !TimeoutActionRecorded(action, reason) {
		return phase
	}
	outcome := TimeoutOutcomeFromAction(action, reason)
	if err := r.tracker.RecordTimeout(TimeoutRecord{
		EscrowID:      escrowID,
		Nonce:         nonce,
		Kind:          TimeoutKind(kind),
		Phase:         phase,
		Outcome:       outcome,
		Reason:        TimeoutReasonFromString(outcome, timeoutReason),
		FailureOrigin: FailureOriginFromDetail(detailReason),
		DetailReason:  detailReason,
		Trace:         TraceRefFromContext(ctx),
	}); err != nil {
		log.Printf("gateway accounting timeout escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
	return phase
}

// Finalize syncs in memory without writing: it runs before the settlement JSON
// and the chain broadcast, and a snapshot there would put a full-table sqlite
// rewrite ahead of settlement. Settled, Retire, Close, and the tick persist.
func (r *Recorder) Finalize(escrowID string) {
	r.sync(escrowID, "")
}

// Settled records the terminal phase, then releases the protocol view: counters
// live in the tracker from here on, and holding the state machine would pin
// every inference record of a rotated escrow for the process lifetime.
func (r *Recorder) Settled(escrowID string) {
	r.syncAndFlush(escrowID, EscrowSettled, "settle")
	r.Release(escrowID)
}

// Retire is the same release for a runtime that goes away without settling.
func (r *Recorder) Retire(escrowID string) {
	r.syncAndFlush(escrowID, "", "retire")
	r.Release(escrowID)
}

func (r *Recorder) Release(escrowID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.states, escrowID)
	r.mu.Unlock()
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
	r.mu.Lock()
	r.states = make(map[string]ProtocolView)
	r.mu.Unlock()
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
	// An explicit phase is recorded even without a protocol view, so a terminal
	// phase does not depend on whether the view was released first.
	if state != nil {
		snapshot := state.SnapshotStateNoInferences()
		if err := r.tracker.SyncState(escrowID, snapshot.LatestNonce, snapshot.HostStats); err != nil {
			log.Printf("gateway accounting sync escrow=%s: %v", escrowID, err)
		}
		if phase == "" {
			phase = escrowPhase(snapshot.Phase)
		}
	}
	if phase == "" {
		return
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

// TimeoutActionRecorded reports whether a gateway timeout action becomes an
// accounting fact. Callers that mirror the outcome onto a span consult it too,
// so the span never claims a dimension the counter does not carry.
func TimeoutActionRecorded(action, reason string) bool {
	if action == "started" {
		return false
	}
	if action != "skipped" {
		return true
	}
	switch reason {
	case "nonce_already_finished", "empty_stream_without_non_empty_winner":
		return false
	default:
		return true
	}
}
