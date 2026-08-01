package accounting

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"devshard/types"
)

type Book struct {
	mu           sync.RWMutex
	escrows      map[string]*escrowBook
	updatedAt    time.Time
	eventErrors  uint64
	writerErrors uint64
	diagnostics  diagnosticRing
	blockHeight  func() int64
}

type escrowBook struct {
	metadata       EscrowMetadata
	latest         uint64
	reducedThrough uint64
	appliedAhead   map[uint64]struct{}
	hostStats      map[uint32]types.HostStats
	counters       map[CounterKey]uint64
	live           map[uint64]*nonceState
	// challenges holds the open challenges by nonce. The unresolved count per
	// slot is derived from it, so the two can never drift apart.
	challenges    map[uint64]uint32
	verdicts      map[uint64]ProtocolTransitionKind
	invalidBySlot map[uint32]uint64
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

func NewBook() *Book {
	return &Book{
		escrows:   make(map[string]*escrowBook),
		updatedAt: time.Now().UTC(),
	}
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
	return b.applyLocked(event)
}

func (b *Book) ApplyBatch(events []Event) error {
	normalized := make([]Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			return errors.New("accounting event is nil")
		}
		value, err := eventValue(event)
		if err != nil {
			return err
		}
		normalized = append(normalized, value)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, event := range normalized {
		if err := b.applyLocked(event); err != nil {
			return err
		}
	}
	return nil
}

func (b *Book) applyLocked(event Event) error {
	var err error
	switch e := event.(type) {
	case EscrowRegistered:
		err = b.registerEscrow(e.Metadata)
	case EscrowPhaseChanged:
		err = b.withEscrow(e.EscrowID, func(escrow *escrowBook) error {
			if !validEscrowPhase(e.Phase) {
				return fmt.Errorf("invalid escrow phase %q", e.Phase)
			}
			if escrowPhaseRank(e.Phase) > escrowPhaseRank(escrow.metadata.Phase) {
				escrow.metadata.Phase = e.Phase
			}
			if e.Phase == EscrowFinalized || e.Phase == EscrowSettled {
				escrow.compactAllTimeouts()
			}
			return nil
		})
	case DiffApplied:
		err = b.applyDiff(e)
	case LatestNonceObserved:
		err = b.withEscrow(e.EscrowID, func(escrow *escrowBook) error {
			if e.LatestNonce > escrow.latest {
				escrow.latest = e.LatestNonce
			}
			if e.LatestNonce > escrow.reducedThrough {
				escrow.reducedThrough = e.LatestNonce
				for nonce := range escrow.appliedAhead {
					if nonce <= e.LatestNonce {
						delete(escrow.appliedAhead, nonce)
					}
				}
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
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageWinner)
	case Loser:
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageLoser)
	case UsageUnknown:
		err = b.applyUsage(e.EscrowID, e.Nonce, UsageUnknownValue)
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
	} else {
		b.appendDiagnostic(event)
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
		if escrowPhaseRank(normalized.Phase) > escrowPhaseRank(existing.metadata.Phase) {
			existing.metadata.Phase = normalized.Phase
		}
		return nil
	}
	b.escrows[normalized.EscrowID] = &escrowBook{
		metadata:      normalized,
		appliedAhead:  make(map[uint64]struct{}),
		hostStats:     make(map[uint32]types.HostStats),
		counters:      make(map[CounterKey]uint64),
		live:          make(map[uint64]*nonceState),
		challenges:    make(map[uint64]uint32),
		verdicts:      make(map[uint64]ProtocolTransitionKind),
		invalidBySlot: make(map[uint32]uint64),
	}
	return nil
}

func (b *Book) withEscrow(escrowID string, apply func(*escrowBook) error) error {
	escrow, err := b.escrow(escrowID)
	if err != nil {
		return err
	}
	return apply(escrow)
}

func (b *Book) applyHostStats(event HostStatsObserved) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
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
