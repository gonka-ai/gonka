package accounting

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"devshard/types"
)

const DefaultSnapshotInterval = 5 * time.Minute

type Tracker struct {
	mu       sync.RWMutex
	store    *Store
	escrows  map[string]*escrowState
	updated  time.Time
	stop     context.CancelFunc
	done     chan struct{}
	once     sync.Once
	now      func() time.Time
	errCount uint64
	wrCount  uint64
}

type escrowState struct {
	Meta            EscrowMetadata             `json:"meta"`
	Latest          uint64                     `json:"latest"`
	HostStats       map[uint32]types.HostStats `json:"host_stats"`
	Counters        map[CounterKey]uint64      `json:"counters"`
	OpenChallenge   map[uint64]uint32          `json:"-"`
	ChallengeBySlot map[uint32]uint64          `json:"challenge_by_slot"`
	InvalidBySlot   map[uint32]uint64          `json:"invalid_by_slot"`
	Live            map[uint64]*nonceState     `json:"-"`
}

type nonceState struct {
	SlotID            uint32
	Inference         bool
	Sent              bool
	Finished          bool
	Receipt           bool
	Usage             Usage
	Ghost             bool
	DispatchPhase     Phase
	Quarantine        QuarantineMode
	NoSendReason      NoSendReason
	FailureOrigin     FailureOrigin
	DetailReason      string
	TimeoutKind       TimeoutKind
	TimeoutPhase      Phase
	TimeoutOutcome    TimeoutOutcome
	TimeoutReason     TimeoutReason
	SendAt            time.Time
	ReceiptAt         int64
	ProtocolTimedOut  bool
	TimeoutResultSeen bool
	Counted           *CounterKey
}

func OpenTracker(path string, retention uint64, interval time.Duration) (*Tracker, error) {
	store, err := OpenStore(path, retention)
	if err != nil {
		return nil, err
	}
	t := &Tracker{
		store:   store,
		escrows: make(map[string]*escrowState),
		updated: time.Now().UTC(),
		now:     time.Now,
	}
	if err := store.Load(context.Background(), t); err != nil {
		store.Close()
		return nil, err
	}
	if interval <= 0 {
		interval = DefaultSnapshotInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.stop = cancel
	t.done = make(chan struct{})
	go t.snapshotLoop(ctx, interval)
	return t, nil
}

func (t *Tracker) snapshotLoop(ctx context.Context, interval time.Duration) {
	defer close(t.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := t.Flush(ctx); err != nil {
				log.Printf("accounting snapshot: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (t *Tracker) Close() error {
	if t == nil {
		return nil
	}
	var err error
	t.once.Do(func() {
		if t.stop != nil {
			t.stop()
			<-t.done
		}
		if flushErr := t.Flush(context.Background()); flushErr != nil {
			err = flushErr
		}
		if closeErr := t.store.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	})
	return err
}

func (t *Tracker) Flush(ctx context.Context) error {
	if t == nil || t.store == nil {
		return nil
	}
	if err := t.store.Save(ctx, t); err != nil {
		t.mu.Lock()
		t.wrCount++
		t.updated = time.Now().UTC()
		t.mu.Unlock()
		return err
	}
	return nil
}

func (t *Tracker) RegisterEscrow(meta EscrowMetadata) error {
	return t.withWrite(func() error {
		meta, err := normalizeMetadata(meta)
		if err != nil {
			return err
		}
		if existing := t.escrows[meta.EscrowID]; existing != nil {
			if meta.CreationEpoch == 0 {
				meta.CreationEpoch = existing.Meta.CreationEpoch
			}
			if existing.Meta.RefusalTimeout == 0 &&
				existing.Meta.ExecutionTimeout == 0 &&
				existing.Meta.TimeoutBufferSeconds == 0 {
				existing.Meta.RefusalTimeout = meta.RefusalTimeout
				existing.Meta.ExecutionTimeout = meta.ExecutionTimeout
				existing.Meta.TimeoutBufferSeconds = meta.TimeoutBufferSeconds
			}
			if !sameMetadata(existing.Meta, meta) {
				return fmt.Errorf("escrow %q already registered with different metadata", meta.EscrowID)
			}
			if phaseRank(meta.Phase) > phaseRank(existing.Meta.Phase) {
				existing.Meta.Phase = meta.Phase
			}
			return nil
		}
		t.escrows[meta.EscrowID] = &escrowState{
			Meta:            meta,
			HostStats:       make(map[uint32]types.HostStats),
			Counters:        make(map[CounterKey]uint64),
			OpenChallenge:   make(map[uint64]uint32),
			ChallengeBySlot: make(map[uint32]uint64),
			InvalidBySlot:   make(map[uint32]uint64),
			Live:            make(map[uint64]*nonceState),
		}
		return nil
	})
}

func (t *Tracker) RecordPhase(escrowID string, phase EscrowPhase) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if !validPhase(phase) {
			return fmt.Errorf("invalid phase %q", phase)
		}
		if phaseRank(phase) > phaseRank(e.Meta.Phase) {
			e.Meta.Phase = phase
		}
		if phase == EscrowFinalized || phase == EscrowSettled {
			e.Live = make(map[uint64]*nonceState)
			e.OpenChallenge = make(map[uint64]uint32)
		}
		return nil
	})
}

func (t *Tracker) RecordDiff(escrowID string, nonce uint64, hasStart bool) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if nonce == 0 {
			return errors.New("nonce must be greater than zero")
		}
		e.recordDiff(nonce, hasStart)
		return nil
	})
}

func (t *Tracker) RecordCommittedDiff(escrowID string, diff types.Diff, verdicts []VerdictRecord) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if diff.Nonce == 0 {
			return errors.New("nonce must be greater than zero")
		}
		if diff.Nonce <= e.Latest {
			return nil
		}

		hasStart := false
		for _, tx := range diff.Txs {
			if start := tx.GetStartInference(); start != nil && start.InferenceId == diff.Nonce {
				hasStart = true
				break
			}
		}
		e.recordDiff(diff.Nonce, hasStart)

		now := t.nowUTC()
		for _, tx := range diff.Txs {
			if msg := tx.GetConfirmStart(); msg != nil {
				if state := e.Live[msg.InferenceId]; state != nil {
					state.Receipt = true
					state.ReceiptAt = msg.ConfirmedAt
					e.reclassify(msg.InferenceId, state, now)
				}
				continue
			}
			if msg := tx.GetFinishInference(); msg != nil {
				if state := e.Live[msg.InferenceId]; state != nil {
					state.markFinished()
					e.reclassify(msg.InferenceId, state, now)
				}
				continue
			}
			if msg := tx.GetTimeoutInference(); msg != nil {
				if state := e.Live[msg.InferenceId]; state != nil {
					state.markProtocolTimeout()
					e.reclassify(msg.InferenceId, state, now)
				}
			}
		}
		for _, verdict := range verdicts {
			if int(verdict.Slot) >= len(e.Meta.Slots) {
				return fmt.Errorf("slot %d out of range", verdict.Slot)
			}
			switch verdict.Kind {
			case ProtocolChallenged:
				if _, ok := e.OpenChallenge[verdict.Nonce]; !ok {
					e.OpenChallenge[verdict.Nonce] = verdict.Slot
					e.ChallengeBySlot[verdict.Slot]++
				}
			case ProtocolValidated:
				e.closeChallenge(verdict.Nonce, verdict.Slot)
			case ProtocolInvalidated:
				slot := e.closeChallenge(verdict.Nonce, verdict.Slot)
				e.InvalidBySlot[slot]++
			}
		}
		return nil
	})
}

func (t *Tracker) RecordProtocol(escrowID string, nonce uint64, slot uint32, kind ProtocolKind, stats types.HostStats) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if int(slot) >= len(e.Meta.Slots) {
			return fmt.Errorf("slot %d out of range", slot)
		}
		e.HostStats[slot] = maxHostStats(e.HostStats[slot], stats)
		switch kind {
		case ProtocolReceiptApplied:
			if s := e.Live[nonce]; s != nil {
				s.Receipt = true
				e.reclassify(nonce, s, t.nowUTC())
			}
		case ProtocolFinishApplied:
			if s := e.Live[nonce]; s != nil {
				s.markFinished()
				e.reclassify(nonce, s, t.nowUTC())
			}
		case ProtocolTimeoutApplied:
			if s := e.Live[nonce]; s != nil {
				s.markProtocolTimeout()
				e.reclassify(nonce, s, t.nowUTC())
			}
		case ProtocolChallenged:
			if _, ok := e.OpenChallenge[nonce]; !ok {
				e.OpenChallenge[nonce] = slot
				e.ChallengeBySlot[slot]++
			}
		case ProtocolValidated:
			e.closeChallenge(nonce, slot)
		case ProtocolInvalidated:
			challengeSlot := e.closeChallenge(nonce, slot)
			e.InvalidBySlot[challengeSlot]++
		default:
			return fmt.Errorf("invalid protocol kind %q", kind)
		}
		return nil
	})
}

func (t *Tracker) RecordReceipt(escrowID string, nonce uint64, confirmedAt int64) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if s := e.Live[nonce]; s != nil {
			s.Receipt = true
			s.ReceiptAt = confirmedAt
			e.reclassify(nonce, s, t.nowUTC())
		}
		return nil
	})
}

func (t *Tracker) RecordHostStats(escrowID string, slot uint32, stats types.HostStats) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if int(slot) >= len(e.Meta.Slots) {
			return fmt.Errorf("slot %d out of range", slot)
		}
		e.HostStats[slot] = maxHostStats(e.HostStats[slot], stats)
		return nil
	})
}

func (t *Tracker) SyncState(escrowID string, latest uint64, hostStats map[uint32]*types.HostStats) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		if latest > e.Latest {
			e.Latest = latest
		}
		for slot, stats := range hostStats {
			if stats == nil {
				continue
			}
			if int(slot) >= len(e.Meta.Slots) {
				return fmt.Errorf("slot %d out of range", slot)
			}
			e.HostStats[slot] = maxHostStats(e.HostStats[slot], *stats)
		}
		e.reconcileAppliedMisses()
		return nil
	})
}

func (t *Tracker) ReconcileAppliedMisses(escrowID string) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		e.reconcileAppliedMisses()
		return nil
	})
}

func (t *Tracker) RecordGhost(escrowID string, nonce uint64, phase Phase, quarantine QuarantineMode, reason NoSendReason, detail string) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		s, err := e.liveNonce(nonce)
		if err != nil {
			return err
		}
		s.Ghost = true
		s.DispatchPhase = normalizePhase(phase)
		s.Quarantine = normalizeQuarantine(quarantine)
		s.NoSendReason = normalizeNoSendReason(reason)
		s.DetailReason = normalizeDetailReason(detail)
		e.reclassify(nonce, s, t.nowUTC())
		return nil
	})
}

func (t *Tracker) RecordRealSend(escrowID string, nonce uint64, sentAt time.Time, phase Phase, quarantine QuarantineMode) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		s, err := e.liveNonce(nonce)
		if err != nil {
			return err
		}
		s.Sent = true
		s.SendAt = sentAt.UTC()
		s.DispatchPhase = normalizePhase(phase)
		s.Quarantine = normalizeQuarantine(quarantine)
		e.reclassify(nonce, s, t.nowUTC())
		return nil
	})
}

func (t *Tracker) RecordUsage(escrowID string, nonce uint64, usage Usage) error {
	return t.withEscrow(escrowID, func(e *escrowState) error {
		s, err := e.liveNonce(nonce)
		if err != nil {
			return err
		}
		s.Usage = normalizeUsage(usage)
		e.reclassify(nonce, s, t.nowUTC())
		return nil
	})
}

func (t *Tracker) RecordTimeout(record TimeoutRecord) error {
	return t.withEscrow(record.EscrowID, func(e *escrowState) error {
		s, err := e.liveNonce(record.Nonce)
		if err != nil {
			return err
		}
		if !s.Sent {
			return errors.New("timeout recorded before real send")
		}
		s.TimeoutKind = normalizeTimeoutKind(record.Kind)
		s.TimeoutPhase = normalizePhase(record.Phase)
		s.TimeoutOutcome = normalizeTimeoutOutcome(record.Outcome)
		s.TimeoutReason = normalizeTimeoutReason(record.Reason)
		s.FailureOrigin = normalizeFailureOrigin(record.FailureOrigin, record.DetailReason)
		s.DetailReason = normalizeDetailReason(record.DetailReason)
		s.TimeoutResultSeen = true
		if s.ProtocolTimedOut {
			s.TimeoutOutcome = TimeoutApplied
		}
		e.reclassify(record.Nonce, s, t.nowUTC())
		return nil
	})
}

func (t *Tracker) withWrite(fn func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := fn()
	t.updated = t.nowUTC()
	if err != nil {
		t.errCount++
	}
	return err
}

func (t *Tracker) nowUTC() time.Time {
	if t.now == nil {
		return time.Now().UTC()
	}
	return t.now().UTC()
}

func (t *Tracker) withEscrow(escrowID string, fn func(*escrowState) error) error {
	return t.withWrite(func() error {
		escrowID = strings.TrimSpace(escrowID)
		if escrowID == "" {
			return errors.New("escrow id is required")
		}
		e := t.escrows[escrowID]
		if e == nil {
			return fmt.Errorf("escrow %q not registered", escrowID)
		}
		return fn(e)
	})
}

func (e *escrowState) liveNonce(nonce uint64) (*nonceState, error) {
	if nonce == 0 || nonce > e.Latest {
		return nil, fmt.Errorf("nonce %d is not consumed", nonce)
	}
	s := e.Live[nonce]
	if s == nil || !s.Inference {
		return nil, fmt.Errorf("nonce %d is not a live inference", nonce)
	}
	return s, nil
}

func (e *escrowState) recordDiff(nonce uint64, hasStart bool) {
	if nonce <= e.Latest {
		return
	}
	e.Latest = nonce
	slot := AssignedNonceSlot(nonce, uint64(len(e.Meta.Slots)))
	if !hasStart {
		e.add(CounterKey{SlotID: slot, Disposition: DispositionProtocolOnly}, 1)
		return
	}
	if _, exists := e.Live[nonce]; !exists {
		e.Live[nonce] = &nonceState{
			SlotID:     slot,
			Inference:  true,
			Quarantine: QuarantineNone,
		}
	}
}

func (e *escrowState) reclassify(nonce uint64, s *nonceState, now time.Time) {
	key, classified := s.counterKey(e.Meta, now)
	if classified && !s.persistable(key) {
		classified = false
	}
	if s.Counted != nil && classified && *s.Counted == key {
		if s.terminal() {
			delete(e.Live, nonce)
		}
		return
	}
	if s.Counted != nil {
		e.remove(*s.Counted)
		s.Counted = nil
	}
	if classified {
		e.add(key, 1)
		s.Counted = &key
	}
	if s.terminal() {
		delete(e.Live, nonce)
	}
}

func (e *escrowState) refreshDerived(now time.Time) {
	for nonce, state := range e.Live {
		e.reclassify(nonce, state, now)
	}
}

func (e *escrowState) add(key CounterKey, delta uint64) {
	e.Counters[key] += delta
}

func (e *escrowState) remove(key CounterKey) {
	if e.Counters[key] <= 1 {
		delete(e.Counters, key)
		return
	}
	e.Counters[key]--
}

func (e *escrowState) closeChallenge(nonce uint64, fallback uint32) uint32 {
	slot := fallback
	if got, ok := e.OpenChallenge[nonce]; ok {
		slot = got
		delete(e.OpenChallenge, nonce)
	}
	if e.ChallengeBySlot[slot] > 0 {
		e.ChallengeBySlot[slot]--
	}
	return slot
}

func (e *escrowState) timeoutAppliedForSlot(slot uint32) uint64 {
	var count uint64
	for key, value := range e.Counters {
		if key.SlotID == slot && key.TimeoutOutcome == TimeoutApplied {
			count += value
		}
	}
	return count
}

func (e *escrowState) reconcileAppliedMisses() {
	for slot, stats := range e.HostStats {
		applied := e.timeoutAppliedForSlot(slot)
		if uint64(stats.Missed) <= applied {
			continue
		}
		e.add(CounterKey{
			SlotID:         slot,
			Disposition:    DispositionUnfinishedRefused,
			TimeoutOutcome: TimeoutApplied,
		}, uint64(stats.Missed)-applied)
	}
}

func (s *nonceState) markFinished() {
	s.Finished = true
	if s.ProtocolTimedOut {
		return
	}
	s.TimeoutKind = ""
	s.TimeoutPhase = ""
	s.TimeoutOutcome = ""
	s.TimeoutReason = ""
	s.TimeoutResultSeen = false
}

func (s *nonceState) markProtocolTimeout() {
	s.ProtocolTimedOut = true
	s.TimeoutOutcome = TimeoutApplied
}

func (s *nonceState) counterKey(meta EscrowMetadata, now time.Time) (CounterKey, bool) {
	key := CounterKey{
		SlotID:                 s.SlotID,
		DispatchPhase:          s.DispatchPhase,
		TimeoutEvaluationPhase: s.TimeoutPhase,
		QuarantineMode:         s.Quarantine,
		NoSendReason:           s.NoSendReason,
		FailureOrigin:          s.FailureOrigin,
		DetailReason:           s.DetailReason,
		TimeoutKind:            s.TimeoutKind,
		TimeoutOutcome:         s.TimeoutOutcome,
		TimeoutReason:          s.TimeoutReason,
	}
	switch {
	case s.Ghost:
		key.Disposition = DispositionGhost
	case s.Finished && s.Usage == UsageWinner:
		key.Disposition = DispositionFinishedUsed
	case s.Finished && s.Usage == UsageLoser:
		key.Disposition = DispositionFinishedUnused
	case s.Finished && s.Usage == UsageUnknownValue:
		key.Disposition = DispositionFinishedUsageUnknown
	case s.Sent && !s.Finished && s.deadlineReached(meta, now) && s.Receipt:
		key.Disposition = DispositionUnfinishedExecution
	case s.Sent && !s.Finished && s.deadlineReached(meta, now):
		key.Disposition = DispositionUnfinishedRefused
	default:
		return CounterKey{}, false
	}
	return key, true
}

func (s *nonceState) terminal() bool {
	return s.Ghost ||
		(s.Finished && s.Usage != "") ||
		(s.ProtocolTimedOut && s.TimeoutResultSeen)
}

func (s *nonceState) persistable(key CounterKey) bool {
	switch key.Disposition {
	case DispositionUnfinishedRefused, DispositionUnfinishedExecution:
		return s.TimeoutResultSeen || s.ProtocolTimedOut
	default:
		return true
	}
}

func (s *nonceState) deadlineReached(meta EscrowMetadata, now time.Time) bool {
	if s.SendAt.IsZero() {
		return false
	}
	buffer := time.Duration(meta.TimeoutBufferSeconds) * time.Second
	if s.Receipt && s.ReceiptAt > 0 {
		deadline := time.Unix(s.ReceiptAt, 0).Add(time.Duration(meta.ExecutionTimeout)*time.Second + buffer)
		return !now.Before(deadline)
	}
	deadline := s.SendAt.Add(time.Duration(meta.RefusalTimeout)*time.Second + buffer)
	return !now.Before(deadline)
}

func AssignedNonceSlot(nonce, slots uint64) uint32 {
	if nonce == 0 || slots == 0 {
		return 0
	}
	return uint32(nonce % slots)
}

func AssignedNoncesForSlot(latest, slots uint64, slotID uint32) (uint64, error) {
	if slots == 0 {
		return 0, errors.New("slot count cannot be zero")
	}
	if uint64(slotID) >= slots {
		return 0, fmt.Errorf("slot %d out of range", slotID)
	}
	first := uint64(slotID)
	if slotID == 0 {
		first = slots
	}
	if latest < first {
		return 0, nil
	}
	return 1 + (latest-first)/slots, nil
}

func normalizeMetadata(meta EscrowMetadata) (EscrowMetadata, error) {
	meta.EscrowID = strings.TrimSpace(meta.EscrowID)
	meta.Model = strings.TrimSpace(meta.Model)
	if meta.EscrowID == "" || meta.Model == "" {
		return EscrowMetadata{}, errors.New("escrow id and model are required")
	}
	if err := types.ValidateGroup(meta.Slots); err != nil {
		return EscrowMetadata{}, err
	}
	if meta.Phase == "" {
		meta.Phase = EscrowActive
	}
	if !validPhase(meta.Phase) {
		return EscrowMetadata{}, fmt.Errorf("invalid phase %q", meta.Phase)
	}
	if meta.RefusalTimeout < 0 || meta.ExecutionTimeout < 0 || meta.TimeoutBufferSeconds < 0 {
		return EscrowMetadata{}, errors.New("timeout values cannot be negative")
	}
	meta.Slots = append([]types.SlotAssignment(nil), meta.Slots...)
	return meta, nil
}

func sameMetadata(a, b EscrowMetadata) bool {
	if a.EscrowID != b.EscrowID ||
		a.CreationEpoch != b.CreationEpoch ||
		a.Model != b.Model ||
		a.RefusalTimeout != b.RefusalTimeout ||
		a.ExecutionTimeout != b.ExecutionTimeout ||
		a.TimeoutBufferSeconds != b.TimeoutBufferSeconds ||
		len(a.Slots) != len(b.Slots) {
		return false
	}
	for i := range a.Slots {
		if a.Slots[i] != b.Slots[i] {
			return false
		}
	}
	return true
}

func validPhase(p EscrowPhase) bool {
	return p == EscrowActive || p == EscrowFinalizing || p == EscrowFinalized || p == EscrowSettled
}

func phaseRank(p EscrowPhase) int {
	switch p {
	case EscrowSettled:
		return 3
	case EscrowFinalized:
		return 2
	case EscrowFinalizing:
		return 1
	default:
		return 0
	}
}

func maxHostStats(a, b types.HostStats) types.HostStats {
	a.Missed = max(a.Missed, b.Missed)
	a.Invalid = max(a.Invalid, b.Invalid)
	a.Cost = max(a.Cost, b.Cost)
	a.RequiredValidations = max(a.RequiredValidations, b.RequiredValidations)
	a.CompletedValidations = max(a.CompletedValidations, b.CompletedValidations)
	return a
}

func normalizePhase(p Phase) Phase {
	if p == PhasePoC || p == PhaseConfirmationPoC {
		return p
	}
	return PhaseNormal
}

func normalizeQuarantine(q QuarantineMode) QuarantineMode {
	switch q {
	case QuarantineProbe, QuarantineShadow, QuarantineProbation:
		return q
	default:
		return QuarantineNone
	}
}

func normalizeNoSendReason(r NoSendReason) NoSendReason {
	switch r {
	case NoSendPoCUnavailable, NoSendParticipantThrottled, NoSendParticipantCapability, NoSendNoCompatibleAfterStale:
		return r
	default:
		return NoSendUnknown
	}
}

func normalizeUsage(u Usage) Usage {
	if u == UsageWinner || u == UsageLoser {
		return u
	}
	return UsageUnknownValue
}

func normalizeTimeoutKind(k TimeoutKind) TimeoutKind {
	if k == TimeoutExecution {
		return TimeoutExecution
	}
	return TimeoutRefused
}

func normalizeTimeoutOutcome(o TimeoutOutcome) TimeoutOutcome {
	switch o {
	case TimeoutSkipped, TimeoutVoteCollectionFailed, TimeoutInsufficientVotes, TimeoutDiffSendFailed, TimeoutApplied:
		return o
	default:
		return TimeoutVoteCollectionFailed
	}
}

func normalizeTimeoutReason(r TimeoutReason) TimeoutReason {
	switch r {
	case TimeoutPhaseTransitionAborted, TimeoutLongResponseAfterContent, TimeoutStateRootDiverged, TimeoutContextCanceled, TimeoutDiffDeliveryFailed, TimeoutNotApplied:
		return r
	default:
		if r == "" {
			return ""
		}
		return TimeoutReasonUnknown
	}
}

func normalizeFailureOrigin(origin FailureOrigin, detail string) FailureOrigin {
	switch origin {
	case FailureHostResponse, FailureGatewayPolicy, FailureClient:
		return origin
	}
	switch {
	case detail == "context_canceled" || strings.Contains(detail, "client"):
		return FailureClient
	case detail == "phase_transition_aborted" || detail == "long_response_after_content" || detail == "timeout_not_applied":
		return FailureGatewayPolicy
	case detail == "not_finished" || detail == "escrow_state_root_diverged" || strings.Contains(detail, "http_") || strings.Contains(detail, "stream"):
		return FailureHostResponse
	default:
		return FailureTransportUnknown
	}
}

func normalizeDetailReason(reason string) string {
	reason = strings.TrimSpace(reason)
	switch reason {
	case "", "none":
		return ""
	case "phase_transition_aborted", "error_stream", "empty_stream", "sse_truncated",
		"eof_transport", "client_cancelled", "transport_error", "no_receipt",
		"not_finished", "http_429", "http_503", "http_forbidden", "http_not_found",
		"http_timestamp_drift", "http_error", "long_response_after_content",
		"escrow_state_root_diverged", "context_canceled", "timeout_diff_delivery_failed",
		"timeout_not_applied", "poc_unavailable_host", "participant_throttled_no_send",
		"participant_capability_no_send", "no_compatible_request_after_stale":
		return reason
	default:
		return "unknown"
	}
}

func sortedCounterKeys(m map[CounterKey]uint64) []CounterKey {
	keys := make([]CounterKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return counterSortKey(keys[i]) < counterSortKey(keys[j]) })
	return keys
}

func counterSortKey(key CounterKey) string {
	return strings.Join([]string{
		fmt.Sprint(key.SlotID),
		string(key.Disposition),
		string(key.DispatchPhase),
		string(key.TimeoutEvaluationPhase),
		string(key.QuarantineMode),
		string(key.NoSendReason),
		string(key.FailureOrigin),
		key.DetailReason,
		string(key.TimeoutKind),
		string(key.TimeoutOutcome),
		string(key.TimeoutReason),
	}, "\x00")
}
