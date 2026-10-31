package accounting

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"devshard/types"
)

var ErrUnknownEscrow = errors.New("accounting: unknown escrow")

type Book struct {
	mu               sync.RWMutex
	escrows          map[string]*escrowLedger
	unsaved          map[string]struct{}
	replaceStored    bool
	updatedAtUnsaved bool
	updatedAt        time.Time
	now              func() time.Time
}

type escrowLedger struct {
	metadata              EscrowMetadata
	latest                uint64
	appliedSweepWatermark uint64
	pendingSweepWatermark uint64
	hostStats             map[uint32]types.HostStats
	challenged            map[uint32]uint64
	validations           map[uint32]uint64
	serviceNonces         map[serviceKey]uint64
	timeouts              map[uint32]uint64
	rejected              map[uint32]uint64
	counters              map[CounterKey]uint64
	nonces                map[uint64]*nonceRecord
	costs                 map[uint64]nonceCost
	inputCounts           map[uint64]inputCount
	folded                map[uint32]SlotMoney
	produced              map[uint32]uint64
	firstContent          map[uint32]FirstContentTiming
	firstContentBuckets   FirstContentBuckets
	events                []protocolEvent
	retired               bool

	feePerNonce      uint64
	finalizingFrom   uint64
	inFlightReserved uint64
	challengedCost   uint64

	restoredLatest     uint64
	restoredUnreplayed bool
}

type serviceKey struct {
	slotID  uint32
	purpose ServicePurpose
}

// The only way to build one: a second site that forgot a map would panic on a path with no error to return.
func newEscrowLedger(metadata EscrowMetadata) *escrowLedger {
	return &escrowLedger{
		metadata:            metadata,
		hostStats:           make(map[uint32]types.HostStats),
		challenged:          make(map[uint32]uint64),
		validations:         make(map[uint32]uint64),
		serviceNonces:       make(map[serviceKey]uint64),
		timeouts:            make(map[uint32]uint64),
		rejected:            make(map[uint32]uint64),
		counters:            make(map[CounterKey]uint64),
		nonces:              make(map[uint64]*nonceRecord),
		costs:               make(map[uint64]nonceCost),
		inputCounts:         make(map[uint64]inputCount),
		folded:              make(map[uint32]SlotMoney),
		produced:            make(map[uint32]uint64),
		firstContent:        make(map[uint32]FirstContentTiming),
		firstContentBuckets: make(FirstContentBuckets),
	}
}

type nonceRecord struct {
	requestID         string
	sent              bool
	finished          bool
	acknowledged      bool
	isCounted         bool
	outputAdded       bool
	firstContentTimed bool
	raced             bool
	sealed            bool
	usage             Usage
	ghostReason       string

	timeoutKind   string
	timeoutAction string
	timeoutReason string

	terminal        string
	phase           Phase
	slowReceipt     bool
	slowChunk       bool
	slowDecode      bool
	clockDrifted    bool
	logprobsDecoded bool

	countedAs CounterKey
}

func NewBook(now func() time.Time) *Book {
	if now == nil {
		now = time.Now
	}
	return &Book{escrows: make(map[string]*escrowLedger), unsaved: make(map[string]struct{}), now: now}
}

// Re-opening keeps the counters and pins the epoch at first sighting. See README.md, "How a nonce is classified".
func (b *Book) OpenEscrow(metadata EscrowMetadata) error {
	if metadata.EscrowID == "" {
		return fmt.Errorf("accounting: escrow id is required")
	}
	if len(metadata.Slots) == 0 {
		return fmt.Errorf("accounting: escrow %s has no slots", metadata.EscrowID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, known := b.escrows[metadata.EscrowID]; known {
		pinnedEpoch := existing.metadata.CreationEpoch
		existing.metadata = metadata
		existing.metadata.CreationEpoch = pinnedEpoch
		existing.retired = false
		b.touchLocked(metadata.EscrowID)
		return nil
	}
	b.escrows[metadata.EscrowID] = newEscrowLedger(metadata)
	b.touchLocked(metadata.EscrowID)
	return nil
}

// ResetEpoch drops what the ledger holds for one epoch and reports how many escrows went with it.
func (b *Book) ResetEpoch(epoch uint64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	cleared := 0
	for escrowID, escrow := range b.escrows {
		if escrow.metadata.CreationEpoch == epoch {
			delete(b.escrows, escrowID)
			b.touchLocked(escrowID)
			cleared++
		}
	}
	return cleared
}

func (b *Book) RetireEscrow(escrowID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return
	}
	if escrow.retired {
		b.noteUpdateLocked()
		return
	}
	escrow.retired = true
	escrow.shrinkPerNonceMaps()
	b.touchLocked(escrowID)
}

// MoneyTotals is one escrow's money as the ledger last observed it: reserved by open records, charged on challenged ones, and charged to its slots.
type MoneyTotals struct {
	InFlightReserved uint64
	Challenged       uint64
	Charged          uint64
}

// MoneyTotals reads an escrow's totals from its last observation; known is false for an escrow the ledger never opened.
func (b *Book) MoneyTotals(escrowID string) (MoneyTotals, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return MoneyTotals{}, false
	}
	totals := MoneyTotals{InFlightReserved: escrow.inFlightReserved, Challenged: escrow.challengedCost}
	for _, stats := range escrow.hostStats {
		totals.Charged += stats.Cost
	}
	return totals, true
}

// TakeRestoredWatermark answers once per escrow restored at start-up the latest nonce its saved ledger held. See docs/accounting.md, "Storage".
func (b *Book) TakeRestoredWatermark(escrowID string) (latestNonce uint64, restored bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	escrow, known := b.escrows[escrowID]
	if !known || !escrow.restoredUnreplayed {
		return 0, false
	}
	escrow.restoredUnreplayed = false
	return escrow.restoredLatest, true
}

func (b *Book) RecordValidation(escrowID string, validatorSlot uint32) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		if int(validatorSlot) >= len(escrow.metadata.Slots) {
			return fmt.Errorf("slot %d out of range", validatorSlot)
		}
		escrow.validations[validatorSlot]++
		return nil
	})
}

// RecordServiceNonce files a nonce a diff with no inference spent. See README.md, "Service nonces".
func (b *Book) RecordServiceNonce(escrowID string, nonce uint64, purpose ServicePurpose) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		escrow.raiseLatest(nonce)
		if purpose == ServiceFinalize {
			escrow.noteFinalizeStart(nonce)
		}
		if escrow.finalizingFrom != 0 && nonce > escrow.finalizingFrom {
			purpose = ServiceFinalize
		}
		escrow.serviceNonces[serviceKey{slotID: escrow.slotOf(nonce), purpose: purpose}]++
		return nil
	})
}

func (b *Book) RecordAppliedTimeout(escrowID string, nonce uint64) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		escrow.timeouts[escrow.slotOf(nonce)]++
		escrow.appendEvent(nonce, ProtocolTimeoutApplied, b.now())
		return nil
	})
}

func (b *Book) RecordInvalidVerdict(escrowID string, nonce uint64) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		escrow.rejected[escrow.slotOf(nonce)]++
		escrow.appendEvent(nonce, ProtocolInvalidated, b.now())
		return nil
	})
}

func (b *Book) RecordAssigned(escrowID string, nonce uint64, requestID string) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		record := escrow.record(nonce)
		record.sent = true
		if record.requestID == "" {
			record.requestID = requestID
		}
		return nil
	})
}

func (b *Book) RecordGhost(escrowID string, nonce uint64, reason string) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		record := escrow.record(nonce)
		record.ghostReason = reason
		delete(escrow.inputCounts, nonce)
		escrow.reclassify(nonce, record)
		return nil
	})
}

func (b *Book) RecordRace(escrowID string, attempts []Attempt) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		for _, attempt := range attempts {
			record := escrow.record(attempt.Nonce)
			record.requestID = attempt.RequestID
			record.sent = attempt.Sent
			record.acknowledged = attempt.Acknowledged
			record.usage = attempt.Usage
			record.terminal = attempt.Terminal
			record.phase = attempt.Phase
			record.slowReceipt = attempt.SlowReceipt
			record.slowChunk = attempt.SlowChunk
			record.clockDrifted = attempt.ClockDrifted
			record.slowDecode = attempt.SlowDecode
			record.logprobsDecoded = attempt.LogprobsDecoded
			record.raced = true
			escrow.recordInputCount(attempt.Nonce, attempt.InputTokens, attempt.InputTokensSource)
			escrow.addProduced(attempt.Nonce, record, attempt.OutputTokens)
			escrow.addFirstContent(attempt.Nonce, record, attempt.FirstContentDelay)
			escrow.reclassify(attempt.Nonce, record)
			if record.sealed {
				escrow.forgetSealed(attempt.Nonce)
			}
		}
		return nil
	})
}

func (b *Book) RecordTimeout(escrowID string, nonce uint64, kind, action, reason string) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		record := escrow.record(nonce)
		if kind != "" {
			record.timeoutKind = kind
		}
		record.timeoutAction = action
		record.timeoutReason = reason
		escrow.reclassify(nonce, record)
		return nil
	})
}

func (b *Book) withEscrow(escrowID string, apply func(*escrowLedger) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownEscrow, escrowID)
	}
	b.markUnsavedLocked(escrowID)
	if err := apply(escrow); err != nil {
		return err
	}
	b.touchLocked(escrowID)
	return nil
}

func (b *Book) touchLocked(escrowID string) {
	b.noteUpdateLocked()
	b.markUnsavedLocked(escrowID)
}

func (b *Book) noteUpdateLocked() {
	b.updatedAt = b.now().UTC()
	b.updatedAtUnsaved = true
}

func (b *Book) markUnsavedLocked(escrowID string) {
	b.unsaved[escrowID] = struct{}{}
}

// Seeing a nonce raises the assigned watermark too, or the counters outrun the range they are measured against.
func (e *escrowLedger) raiseLatest(nonce uint64) bool {
	if nonce <= e.latest {
		return false
	}
	e.latest = nonce
	return true
}

// Only the first finalize round names itself, so the earliest start either source saw wins.
func (e *escrowLedger) noteFinalizeStart(nonce uint64) bool {
	if nonce == 0 || (e.finalizingFrom != 0 && nonce >= e.finalizingFrom) {
		return false
	}
	e.finalizingFrom = nonce
	return true
}

func (e *escrowLedger) record(nonce uint64) *nonceRecord {
	e.raiseLatest(nonce)
	existing, known := e.nonces[nonce]
	if known {
		return existing
	}
	fresh := &nonceRecord{usage: UsageUnknown}
	e.nonces[nonce] = fresh
	return fresh
}

func (e *escrowLedger) slotOf(nonce uint64) uint32 {
	return uint32(nonce % uint64(len(e.metadata.Slots)))
}

func (e *escrowLedger) foldSealed(inferences map[uint64]*types.InferenceRecord) bool {
	folded := false
	for nonce, cost := range e.costs {
		if _, live := inferences[nonce]; live {
			continue
		}
		if FinishApplied(cost.status) {
			e.markFinished(nonce)
		}
		slotID := e.slotOf(nonce)
		money := e.folded[slotID]
		e.foldCost(&money, nonce, cost)
		e.folded[slotID] = money
		delete(e.costs, nonce)
		e.forgetSealed(nonce)
		folded = true
	}
	return folded
}

func (e *escrowLedger) forgetSealed(nonce uint64) {
	record, known := e.nonces[nonce]
	if !known || revisable(record) {
		return
	}
	if !record.raced && record.countedAs.Disposition == DispositionFinishedUsageUnknown {
		record.sealed = true
		return
	}
	delete(e.nonces, nonce)
}

func sealedMoney(folded map[uint32]SlotMoney) map[uint32]SlotMoney {
	money := make(map[uint32]SlotMoney, len(folded))
	for slotID, slot := range folded {
		if slot != (SlotMoney{}) {
			money[slotID] = slot
		}
	}
	return money
}

// FinishApplied reports a record whose Finish the escrow's log holds. See README.md, "Boundaries".
func FinishApplied(status types.InferenceStatus) bool {
	switch status {
	case types.StatusFinished, types.StatusChallenged, types.StatusValidated, types.StatusInvalidated:
		return true
	}
	return false
}

// UnfinishedNonces lists the dispatched nonces the ledger still holds open or may still revise to finished. See README.md, "Boundaries".
func (b *Book) UnfinishedNonces(escrowID string) []uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return nil
	}
	var unfinished []uint64
	for nonce, record := range escrow.nonces {
		if record.sent && !record.finished && revisable(record) {
			unfinished = append(unfinished, nonce)
		}
	}
	slices.Sort(unfinished)
	return unfinished
}

// MarkFinished records nonces whose Finish the escrow's log holds but which no sweep read live.
func (b *Book) MarkFinished(escrowID string, nonces []uint64) error {
	if len(nonces) == 0 {
		return nil
	}
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		for _, nonce := range nonces {
			escrow.markFinished(nonce)
		}
		return nil
	})
}

func (e *escrowLedger) markFinished(nonce uint64) bool {
	record, known := e.nonces[nonce]
	if !known || record.finished {
		return false
	}
	record.finished = true
	e.reclassify(nonce, record)
	return true
}
