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
			verdicts = append(verdicts, VerdictRecord{
				Nonce: inferenceID,
				Slot:  record.ExecutorSlot,
				Kind:  ProtocolTimeoutApplied,
			})
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

// RequestID names the client request a nonce came from, so a later miss or invalid can point at it.
func (r *Recorder) RequestID(escrowID string, nonce uint64, requestID string) {
	if r == nil || r.tracker == nil || requestID == "" {
		return
	}
	if err := r.tracker.RecordRequestID(escrowID, nonce, requestID); err != nil {
		log.Printf("gateway accounting request id escrow=%s nonce=%d: %v", escrowID, nonce, err)
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

func (r *Recorder) Usage(escrowID string, nonce, winnerNonce uint64, deliveryReason string) {
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
	if err := r.tracker.RecordUsage(escrowID, nonce, usage, deliveryReason); err != nil {
		log.Printf("gateway accounting usage escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) LogprobsDecoded(escrowID string, nonce uint64) {
	if r == nil || r.tracker == nil {
		return
	}
	if err := r.tracker.RecordLogprobsDecoded(escrowID, nonce); err != nil {
		log.Printf("gateway accounting logprobs escrow=%s nonce=%d: %v", escrowID, nonce, err)
	}
}

func (r *Recorder) AttemptTiming(escrowID string, nonce uint64, timing AttemptTiming) {
	if r == nil || r.tracker == nil {
		return
	}
	if err := r.tracker.RecordAttemptTiming(escrowID, nonce, timing); err != nil {
		log.Printf("gateway accounting timing escrow=%s nonce=%d: %v", escrowID, nonce, err)
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

func timeoutSkipRequiresAccounting(reason string) bool {
	switch reason {
	case "nonce_already_finished", "empty_stream_without_non_empty_winner":
		return false
	default:
		return true
	}
}
