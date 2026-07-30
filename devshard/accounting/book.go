package accounting

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"devshard/types"
)

const dedupeWindow = 4096

type Book struct {
	mu           sync.RWMutex
	escrows      map[string]*escrowBook
	updatedAt    time.Time
	eventErrors  uint64
	writerErrors uint64
}

type escrowBook struct {
	metadata  EscrowMetadata
	latest    uint64
	hostStats map[uint32]types.HostStats
	counters  map[CounterKey]uint64
	live      map[uint64]*nonceState
	// challenges holds the open challenges by nonce. The unresolved count per
	// slot is derived from it, so the two can never drift apart.
	challenges    map[uint64]uint32
	invalidBySlot map[uint32]uint64
	// seen deduplicates replayed callbacks. Events are used as their own keys,
	// which requires every event type routed through withEscrowEvent to be a
	// comparable struct (TestEventTypesAreComparable enforces this).
	seen      map[Event]struct{}
	seenOrder []Event
}

type nonceState struct {
	slotID          uint32
	inference       bool
	receipt         bool
	finish          bool
	sent            bool
	usage           Usage
	ghost           bool
	dispatchPhase   Phase
	quarantine      QuarantineMode
	noSendReason    NoSendReason
	failureOrigin   FailureOrigin
	detailReason    string
	timeoutRequired bool
	timeoutPhase    Phase
	timeoutKind     TimeoutKind
	timeoutOutcome  TimeoutOutcome
	timeoutReason   TimeoutReason
	counted         *CounterKey
}

type Snapshot struct {
	SchemaVersion int
	UpdatedAt     time.Time
	EventErrors   uint64
	WriterErrors  uint64
	Escrows       []EscrowSnapshot
}

type EscrowSnapshot struct {
	Metadata           EscrowMetadata
	LatestNonce        uint64
	HostStats          map[uint32]types.HostStats
	Counters           map[CounterKey]uint64
	Challenges         map[uint64]uint32
	InvalidTransitions map[uint32]uint64
	Pending            []PendingNonceSnapshot
}

type PendingNonceSnapshot struct {
	Nonce          uint64
	SlotID         uint32
	Receipt        bool
	Sent           bool
	Usage          Usage
	DispatchPhase  Phase
	Quarantine     QuarantineMode
	FailureOrigin  FailureOrigin
	DetailReason   string
	TimeoutPhase   Phase
	TimeoutKind    TimeoutKind
	TimeoutOutcome TimeoutOutcome
	TimeoutReason  TimeoutReason
	Counted        CounterKey
}

func NewBook() *Book {
	return &Book{
		escrows:   make(map[string]*escrowBook),
		updatedAt: time.Now().UTC(),
	}
}

func NewBookFromSnapshot(snapshot Snapshot) (*Book, error) {
	book := NewBook()
	if err := book.Restore(snapshot); err != nil {
		return nil, err
	}
	return book, nil
}

func (b *Book) Apply(event Event) error {
	if event == nil {
		return errors.New("accounting event is nil")
	}
	event, err := eventValue(event)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch e := event.(type) {
	case EscrowRegistered:
		err = b.registerEscrow(e.Metadata)
	case EscrowPhaseChanged:
		err = b.withEscrowEvent(e.EscrowID, e, func(escrow *escrowBook) error {
			if !validEscrowPhase(e.Phase) {
				return fmt.Errorf("invalid escrow phase %q", e.Phase)
			}
			escrow.metadata.Phase = e.Phase
			return nil
		})
	case DiffApplied:
		err = b.applyDiff(e)
	case LatestNonceObserved:
		err = b.withEscrowEvent(e.EscrowID, e, func(escrow *escrowBook) error {
			if e.LatestNonce > escrow.latest {
				escrow.latest = e.LatestNonce
			}
			return nil
		})
	case ProtocolTransition:
		err = b.applyProtocolTransition(e)
	case Ghost:
		err = b.applyGhost(e)
	case RealSend:
		err = b.applyRealSend(e)
	case Winner:
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageWinner, e)
	case Loser:
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageLoser, e)
	case UsageUnknown:
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageUnknownValue, e)
	case TimeoutRequired:
		err = b.applyTimeoutRequired(e)
	case TimeoutOutcomeRecorded:
		err = b.applyTimeoutOutcome(e)
	case HostStatsObserved:
		err = b.applyHostStats(e)
	default:
		err = fmt.Errorf("unsupported accounting event %T", event)
	}
	b.updatedAt = time.Now().UTC()
	if err != nil {
		b.eventErrors++
	}
	return err
}

func eventValue(event Event) (Event, error) {
	switch e := event.(type) {
	case *EscrowRegistered:
		if e != nil {
			return *e, nil
		}
	case *EscrowPhaseChanged:
		if e != nil {
			return *e, nil
		}
	case *DiffApplied:
		if e != nil {
			return *e, nil
		}
	case *LatestNonceObserved:
		if e != nil {
			return *e, nil
		}
	case *ProtocolTransition:
		if e != nil {
			return *e, nil
		}
	case *Ghost:
		if e != nil {
			return *e, nil
		}
	case *RealSend:
		if e != nil {
			return *e, nil
		}
	case *Winner:
		if e != nil {
			return *e, nil
		}
	case *Loser:
		if e != nil {
			return *e, nil
		}
	case *UsageUnknown:
		if e != nil {
			return *e, nil
		}
	case *TimeoutRequired:
		if e != nil {
			return *e, nil
		}
	case *TimeoutOutcomeRecorded:
		if e != nil {
			return *e, nil
		}
	case *HostStatsObserved:
		if e != nil {
			return *e, nil
		}
	default:
		return event, nil
	}
	return nil, errors.New("accounting event is nil")
}

func (b *Book) registerEscrow(metadata EscrowMetadata) error {
	normalized, err := normalizeMetadata(metadata)
	if err != nil {
		return err
	}
	if existing := b.escrows[normalized.EscrowID]; existing != nil {
		if !sameMetadata(existing.metadata, normalized) {
			return fmt.Errorf("escrow %q already registered with different metadata", normalized.EscrowID)
		}
		existing.metadata.Phase = normalized.Phase
		return nil
	}
	b.escrows[normalized.EscrowID] = &escrowBook{
		metadata:      normalized,
		hostStats:     make(map[uint32]types.HostStats),
		counters:      make(map[CounterKey]uint64),
		live:          make(map[uint64]*nonceState),
		challenges:    make(map[uint64]uint32),
		invalidBySlot: make(map[uint32]uint64),
		seen:          make(map[Event]struct{}),
	}
	return nil
}

func (b *Book) withEscrowEvent(escrowID string, event Event, apply func(*escrowBook) error) error {
	escrow, err := b.escrow(escrowID)
	if err != nil {
		return err
	}
	if escrow.wasSeen(event) {
		return nil
	}
	if err := apply(escrow); err != nil {
		return err
	}
	escrow.markSeen(event)
	return nil
}

func (b *Book) applyDiff(event DiffApplied) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		if event.Nonce == 0 {
			return errors.New("diff nonce must be greater than zero")
		}
		if event.Nonce > escrow.latest {
			escrow.latest = event.Nonce
		}
		if _, ok := escrow.live[event.Nonce]; ok {
			return nil
		}
		slotID := AssignedNonceSlot(event.Nonce, uint64(len(escrow.metadata.Slots)))
		if !event.Inference {
			key := CounterKey{
				SlotID:      slotID,
				Disposition: DispositionProtocolOnly,
			}
			escrow.increment(key)
			return nil
		}
		escrow.live[event.Nonce] = &nonceState{
			slotID:     slotID,
			inference:  true,
			quarantine: QuarantineNone,
		}
		return nil
	})
}

func (b *Book) applyProtocolTransition(event ProtocolTransition) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		if event.Nonce == 0 {
			return errors.New("protocol transition nonce must be greater than zero")
		}
		switch event.Kind {
		case ProtocolChallenged:
			if _, exists := escrow.challenges[event.Nonce]; exists {
				return nil
			}
			slotID, err := escrow.slotForNonce(event.Nonce)
			if err != nil {
				return err
			}
			escrow.challenges[event.Nonce] = slotID
			return nil
		case ProtocolValidated:
			if _, err := escrow.slotForNonce(event.Nonce); err != nil {
				return err
			}
			delete(escrow.challenges, event.Nonce)
			return nil
		case ProtocolInvalidated:
			slotID, err := escrow.slotForNonce(event.Nonce)
			if err != nil {
				return err
			}
			if challengedSlot, ok := escrow.challenges[event.Nonce]; ok {
				slotID = challengedSlot
			}
			delete(escrow.challenges, event.Nonce)
			escrow.invalidBySlot[slotID]++
			return nil
		}

		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			return err
		}
		switch event.Kind {
		case ProtocolReceiptApplied:
			state.receipt = true
		case ProtocolFinishApplied:
			state.finish = true
			if state.timeoutRequired && state.timeoutOutcome != TimeoutApplied {
				state.timeoutRequired = false
				state.timeoutKind = ""
				state.timeoutPhase = ""
				state.timeoutOutcome = ""
				state.timeoutReason = ""
			}
		default:
			return fmt.Errorf("invalid protocol transition %q", event.Kind)
		}
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyGhost(event Ghost) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			return err
		}
		phase, err := normalizePhase(event.DispatchPhase)
		if err != nil {
			return err
		}
		quarantine, err := normalizeQuarantine(event.Quarantine)
		if err != nil {
			return err
		}
		state.ghost = true
		state.dispatchPhase = phase
		state.quarantine = quarantine
		state.noSendReason = normalizeNoSendReason(event.Reason)
		state.detailReason = normalizeDetailReason(event.DetailReason)
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyRealSend(event RealSend) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			return err
		}
		phase, err := normalizePhase(event.DispatchPhase)
		if err != nil {
			return err
		}
		quarantine, err := normalizeQuarantine(event.Quarantine)
		if err != nil {
			return err
		}
		state.sent = true
		state.dispatchPhase = phase
		state.quarantine = quarantine
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyUsage(escrowID string, nonce uint64, usage Usage, event Event) error {
	return b.withEscrowEvent(escrowID, event, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(nonce)
		if err != nil {
			return err
		}
		state.usage = usage
		escrow.reclassify(nonce, state)
		return nil
	})
}

func (b *Book) applyTimeoutRequired(event TimeoutRequired) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			return err
		}
		if !state.sent {
			return errors.New("timeout required for a nonce that was not sent")
		}
		if event.Kind != TimeoutRefused && event.Kind != TimeoutExecution {
			return fmt.Errorf("invalid timeout kind %q", event.Kind)
		}
		phase, err := normalizePhase(event.EvaluationPhase)
		if err != nil {
			return err
		}
		origin, err := normalizeFailureOrigin(event.FailureOrigin)
		if err != nil {
			return err
		}
		state.timeoutRequired = true
		state.timeoutKind = event.Kind
		state.timeoutPhase = phase
		state.failureOrigin = origin
		state.detailReason = normalizeDetailReason(event.DetailReason)
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyTimeoutOutcome(event TimeoutOutcomeRecorded) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			return err
		}
		if !state.timeoutRequired {
			return errors.New("timeout outcome recorded before timeout was required")
		}
		if !validTimeoutOutcome(event.Outcome) {
			return fmt.Errorf("invalid timeout outcome %q", event.Outcome)
		}
		state.timeoutOutcome = event.Outcome
		if event.Outcome == TimeoutApplied {
			state.timeoutReason = ""
		} else {
			state.timeoutReason = normalizeTimeoutReason(event.Reason)
		}
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyHostStats(event HostStatsObserved) error {
	return b.withEscrowEvent(event.EscrowID, event, func(escrow *escrowBook) error {
		if int(event.SlotID) >= len(escrow.metadata.Slots) {
			return fmt.Errorf("slot %d out of range", event.SlotID)
		}
		current := escrow.hostStats[event.SlotID]
		current.Missed = max(current.Missed, event.Stats.Missed)
		current.Invalid = max(current.Invalid, event.Stats.Invalid)
		current.Cost = max(current.Cost, event.Stats.Cost)
		current.RequiredValidations = max(current.RequiredValidations, event.Stats.RequiredValidations)
		current.CompletedValidations = max(current.CompletedValidations, event.Stats.CompletedValidations)
		escrow.hostStats[event.SlotID] = current
		return nil
	})
}

func (b *Book) escrow(escrowID string) (*escrowBook, error) {
	escrowID = strings.TrimSpace(escrowID)
	if escrowID == "" {
		return nil, errors.New("escrow id is required")
	}
	escrow := b.escrows[escrowID]
	if escrow == nil {
		return nil, fmt.Errorf("escrow %q is not registered", escrowID)
	}
	return escrow, nil
}

func (e *escrowBook) mutableNonce(nonce uint64) (*nonceState, error) {
	if nonce == 0 || nonce > e.latest {
		return nil, fmt.Errorf("nonce %d is not consumed (latest %d)", nonce, e.latest)
	}
	state := e.live[nonce]
	if state == nil || !state.inference {
		return nil, fmt.Errorf("nonce %d is not a mutable inference nonce", nonce)
	}
	return state, nil
}

func (e *escrowBook) slotForNonce(nonce uint64) (uint32, error) {
	if nonce == 0 || nonce > e.latest {
		return 0, fmt.Errorf("nonce %d is not consumed (latest %d)", nonce, e.latest)
	}
	if state := e.live[nonce]; state != nil {
		return state.slotID, nil
	}
	return AssignedNonceSlot(nonce, uint64(len(e.metadata.Slots))), nil
}

func (e *escrowBook) reclassify(nonce uint64, state *nonceState) {
	if state.counted != nil {
		e.decrement(*state.counted)
		state.counted = nil
	}
	if key, ok := state.counterKey(); ok {
		e.increment(key)
		state.counted = &key
	}
	if state.isTerminal() {
		delete(e.live, nonce)
	}
}

func (s *nonceState) counterKey() (CounterKey, bool) {
	key := CounterKey{
		SlotID:                 s.slotID,
		DispatchPhase:          s.dispatchPhase,
		TimeoutEvaluationPhase: s.timeoutPhase,
		QuarantineMode:         s.quarantine,
		NoSendReason:           s.noSendReason,
		FailureOrigin:          s.failureOrigin,
		DetailReason:           s.detailReason,
		TimeoutKind:            s.timeoutKind,
		TimeoutOutcome:         s.timeoutOutcome,
		TimeoutReason:          s.timeoutReason,
	}
	switch {
	case s.ghost:
		key.Disposition = DispositionGhost
	case s.finish && s.usage == UsageWinner:
		key.Disposition = DispositionFinishedUsed
	case s.finish && s.usage == UsageLoser:
		key.Disposition = DispositionFinishedUnused
	case s.finish && s.usage == UsageUnknownValue:
		key.Disposition = DispositionFinishedUsageUnknown
	case s.timeoutRequired && s.receipt:
		key.Disposition = DispositionUnfinishedExecution
	case s.timeoutRequired:
		key.Disposition = DispositionUnfinishedRefused
	default:
		return CounterKey{}, false
	}
	return key, true
}

func (s *nonceState) isTerminal() bool {
	if s.ghost {
		return true
	}
	if s.finish && s.usage != "" {
		return true
	}
	return s.timeoutRequired && s.timeoutOutcome == TimeoutApplied
}

func (e *escrowBook) increment(key CounterKey) {
	e.counters[key]++
}

func (e *escrowBook) decrement(key CounterKey) {
	if e.counters[key] <= 1 {
		delete(e.counters, key)
		return
	}
	e.counters[key]--
}

func (e *escrowBook) wasSeen(event Event) bool {
	_, ok := e.seen[event]
	return ok
}

func (e *escrowBook) markSeen(event Event) {
	if event == nil {
		return
	}
	if _, exists := e.seen[event]; exists {
		return
	}
	e.seen[event] = struct{}{}
	e.seenOrder = append(e.seenOrder, event)
	if len(e.seenOrder) <= dedupeWindow {
		return
	}
	delete(e.seen, e.seenOrder[0])
	e.seenOrder = e.seenOrder[1:]
}

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
	return phase == EscrowActive || phase == EscrowFinalizing || phase == EscrowSettled
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

func (b *Book) Snapshot() Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshotLocked()
}

func (b *Book) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     b.updatedAt,
		EventErrors:   b.eventErrors,
		WriterErrors:  b.writerErrors,
		Escrows:       make([]EscrowSnapshot, 0, len(b.escrows)),
	}
	for _, escrow := range b.escrows {
		item := EscrowSnapshot{
			Metadata:           cloneMetadata(escrow.metadata),
			LatestNonce:        escrow.latest,
			HostStats:          make(map[uint32]types.HostStats, len(escrow.hostStats)),
			Counters:           make(map[CounterKey]uint64, len(escrow.counters)),
			Challenges:         make(map[uint64]uint32, len(escrow.challenges)),
			InvalidTransitions: make(map[uint32]uint64, len(escrow.invalidBySlot)),
		}
		for slotID, stats := range escrow.hostStats {
			item.HostStats[slotID] = stats
		}
		for key, count := range escrow.counters {
			item.Counters[key] = count
		}
		for nonce, slotID := range escrow.challenges {
			item.Challenges[nonce] = slotID
		}
		for slotID, count := range escrow.invalidBySlot {
			item.InvalidTransitions[slotID] = count
		}
		for nonce, state := range escrow.live {
			if !state.timeoutRequired || state.timeoutOutcome == TimeoutApplied || state.counted == nil {
				continue
			}
			item.Pending = append(item.Pending, PendingNonceSnapshot{
				Nonce:          nonce,
				SlotID:         state.slotID,
				Receipt:        state.receipt,
				Sent:           state.sent,
				Usage:          state.usage,
				DispatchPhase:  state.dispatchPhase,
				Quarantine:     state.quarantine,
				FailureOrigin:  state.failureOrigin,
				DetailReason:   state.detailReason,
				TimeoutPhase:   state.timeoutPhase,
				TimeoutKind:    state.timeoutKind,
				TimeoutOutcome: state.timeoutOutcome,
				TimeoutReason:  state.timeoutReason,
				Counted:        *state.counted,
			})
		}
		sort.Slice(item.Pending, func(i, j int) bool {
			return item.Pending[i].Nonce < item.Pending[j].Nonce
		})
		snapshot.Escrows = append(snapshot.Escrows, item)
	}
	sort.Slice(snapshot.Escrows, func(i, j int) bool {
		return snapshot.Escrows[i].Metadata.EscrowID < snapshot.Escrows[j].Metadata.EscrowID
	})
	return snapshot
}

func (b *Book) Restore(snapshot Snapshot) error {
	if snapshot.SchemaVersion != 0 && snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported accounting schema version %d", snapshot.SchemaVersion)
	}
	restored := make(map[string]*escrowBook, len(snapshot.Escrows))
	for _, item := range snapshot.Escrows {
		metadata, err := normalizeMetadata(item.Metadata)
		if err != nil {
			return err
		}
		if _, exists := restored[metadata.EscrowID]; exists {
			return fmt.Errorf("duplicate escrow %q in snapshot", metadata.EscrowID)
		}
		escrow := &escrowBook{
			metadata:      metadata,
			latest:        item.LatestNonce,
			hostStats:     make(map[uint32]types.HostStats, len(item.HostStats)),
			counters:      make(map[CounterKey]uint64, len(item.Counters)),
			live:          make(map[uint64]*nonceState),
			challenges:    make(map[uint64]uint32, len(item.Challenges)),
			invalidBySlot: make(map[uint32]uint64, len(item.InvalidTransitions)),
			seen:          make(map[Event]struct{}),
		}
		for slotID, stats := range item.HostStats {
			if int(slotID) >= len(metadata.Slots) {
				return fmt.Errorf("host stats slot %d out of range for escrow %q", slotID, metadata.EscrowID)
			}
			escrow.hostStats[slotID] = stats
		}
		for key, count := range item.Counters {
			if count == 0 {
				continue
			}
			if int(key.SlotID) >= len(metadata.Slots) {
				return fmt.Errorf("counter slot %d out of range for escrow %q", key.SlotID, metadata.EscrowID)
			}
			escrow.counters[key] = count
		}
		for nonce, slotID := range item.Challenges {
			if nonce == 0 || nonce > item.LatestNonce {
				return fmt.Errorf("challenged nonce %d out of range for escrow %q", nonce, metadata.EscrowID)
			}
			if int(slotID) >= len(metadata.Slots) {
				return fmt.Errorf("challenged nonce %d slot %d out of range for escrow %q", nonce, slotID, metadata.EscrowID)
			}
			escrow.challenges[nonce] = slotID
		}
		for slotID, count := range item.InvalidTransitions {
			if count == 0 {
				continue
			}
			if int(slotID) >= len(metadata.Slots) {
				return fmt.Errorf("invalid transition slot %d out of range for escrow %q", slotID, metadata.EscrowID)
			}
			escrow.invalidBySlot[slotID] = count
		}
		for _, pending := range item.Pending {
			if pending.Nonce == 0 || pending.Nonce > item.LatestNonce {
				return fmt.Errorf("pending nonce %d out of range for escrow %q", pending.Nonce, metadata.EscrowID)
			}
			if int(pending.SlotID) >= len(metadata.Slots) {
				return fmt.Errorf("pending nonce %d slot %d out of range for escrow %q", pending.Nonce, pending.SlotID, metadata.EscrowID)
			}
			if pending.Counted.SlotID != pending.SlotID {
				return fmt.Errorf("pending nonce %d counter slot does not match escrow %q", pending.Nonce, metadata.EscrowID)
			}
			if escrow.counters[pending.Counted] == 0 {
				return fmt.Errorf("pending nonce %d references a missing counter for escrow %q", pending.Nonce, metadata.EscrowID)
			}
			if _, exists := escrow.live[pending.Nonce]; exists {
				return fmt.Errorf("duplicate pending nonce %d for escrow %q", pending.Nonce, metadata.EscrowID)
			}
			counted := pending.Counted
			escrow.live[pending.Nonce] = &nonceState{
				slotID:          pending.SlotID,
				inference:       true,
				receipt:         pending.Receipt,
				sent:            pending.Sent,
				usage:           pending.Usage,
				dispatchPhase:   pending.DispatchPhase,
				quarantine:      pending.Quarantine,
				failureOrigin:   pending.FailureOrigin,
				detailReason:    pending.DetailReason,
				timeoutRequired: true,
				timeoutPhase:    pending.TimeoutPhase,
				timeoutKind:     pending.TimeoutKind,
				timeoutOutcome:  pending.TimeoutOutcome,
				timeoutReason:   pending.TimeoutReason,
				counted:         &counted,
			}
		}
		restored[metadata.EscrowID] = escrow
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.escrows = restored
	b.eventErrors = snapshot.EventErrors
	b.writerErrors = snapshot.WriterErrors
	b.updatedAt = snapshot.UpdatedAt
	if b.updatedAt.IsZero() {
		b.updatedAt = time.Now().UTC()
	}
	return nil
}

func (b *Book) RecordWriterError() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writerErrors++
	b.updatedAt = time.Now().UTC()
}

func (b *Book) WriterErrors() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.writerErrors
}

func (b *Book) EventErrors() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.eventErrors
}

func (b *Book) Prune(retentionEpochs uint64) {
	if b == nil || retentionEpochs == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var maxEpoch uint64
	complete := make(map[uint64]bool)
	for _, escrow := range b.escrows {
		epoch := escrow.metadata.CreationEpoch
		if epoch > maxEpoch {
			maxEpoch = epoch
		}
		if _, exists := complete[epoch]; !exists {
			complete[epoch] = true
		}
		if escrow.metadata.Phase != EscrowSettled {
			complete[epoch] = false
		}
	}
	var cutoff uint64
	if maxEpoch+1 > retentionEpochs {
		cutoff = maxEpoch + 1 - retentionEpochs
	}
	for escrowID, escrow := range b.escrows {
		if escrow.metadata.CreationEpoch < cutoff && complete[escrow.metadata.CreationEpoch] {
			delete(b.escrows, escrowID)
		}
	}
}

func (b *Book) LiveNonceCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	total := 0
	for _, escrow := range b.escrows {
		total += len(escrow.live)
	}
	return total
}

func cloneMetadata(metadata EscrowMetadata) EscrowMetadata {
	metadata.Slots = append([]types.SlotAssignment(nil), metadata.Slots...)
	return metadata
}
