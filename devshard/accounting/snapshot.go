package accounting

import (
	"fmt"
	"sort"
	"time"

	"devshard/types"
)

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
	ReducedThrough     uint64
	HostStats          map[uint32]types.HostStats
	Counters           map[CounterKey]uint64
	Challenges         map[uint64]uint32
	ValidationVerdicts map[uint64]ProtocolTransitionKind
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

func NewBookFromSnapshot(snapshot Snapshot) (*Book, error) {
	book := NewBook()
	if err := book.Restore(snapshot); err != nil {
		return nil, err
	}
	return book, nil
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
			ReducedThrough:     escrow.reducedThrough,
			HostStats:          make(map[uint32]types.HostStats, len(escrow.hostStats)),
			Counters:           make(map[CounterKey]uint64, len(escrow.counters)),
			Challenges:         make(map[uint64]uint32, len(escrow.challenges)),
			ValidationVerdicts: make(map[uint64]ProtocolTransitionKind, len(escrow.verdicts)),
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
		for nonce, verdict := range escrow.verdicts {
			item.ValidationVerdicts[nonce] = verdict
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
			metadata:       metadata,
			latest:         item.LatestNonce,
			reducedThrough: item.ReducedThrough,
			appliedAhead:   make(map[uint64]struct{}),
			hostStats:      make(map[uint32]types.HostStats, len(item.HostStats)),
			counters:       make(map[CounterKey]uint64, len(item.Counters)),
			live:           make(map[uint64]*nonceState),
			challenges:     make(map[uint64]uint32, len(item.Challenges)),
			verdicts:       make(map[uint64]ProtocolTransitionKind, len(item.ValidationVerdicts)),
			invalidBySlot:  make(map[uint32]uint64, len(item.InvalidTransitions)),
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
		for nonce, verdict := range item.ValidationVerdicts {
			if nonce == 0 || nonce > item.LatestNonce {
				return fmt.Errorf("validation verdict nonce %d out of range for escrow %q", nonce, metadata.EscrowID)
			}
			if verdict != ProtocolValidated && verdict != ProtocolInvalidated {
				return fmt.Errorf("invalid validation verdict %q for escrow %q", verdict, metadata.EscrowID)
			}
			escrow.verdicts[nonce] = verdict
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

func (b *Book) LatestNonce(escrowID string) uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if escrow := b.escrows[escrowID]; escrow != nil {
		return escrow.latest
	}
	return 0
}

func (b *Book) ReducedThrough(escrowID string) uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if escrow := b.escrows[escrowID]; escrow != nil {
		return escrow.reducedThrough
	}
	return 0
}

func (b *Book) sendState(escrowID string, nonce uint64) (exists, sent bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	escrow := b.escrows[escrowID]
	if escrow == nil {
		return false, false
	}
	state := escrow.live[nonce]
	if state == nil {
		return false, false
	}
	return true, state.sent
}

func (b *Book) PendingTimeouts(escrowID string) []PendingNonceSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	escrow := b.escrows[escrowID]
	if escrow == nil {
		return nil
	}
	pending := make([]PendingNonceSnapshot, 0, len(escrow.live))
	for nonce, state := range escrow.live {
		if state.timeoutRequired {
			pending = append(pending, PendingNonceSnapshot{
				Nonce: nonce,
				Usage: state.usage,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Nonce < pending[j].Nonce })
	return pending
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
