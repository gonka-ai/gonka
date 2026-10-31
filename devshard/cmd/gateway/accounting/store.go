package accounting

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/types"
)

type Snapshot struct {
	SchemaVersion int              `json:"schema_version"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Escrows       []EscrowSnapshot `json:"escrows"`
}

type EscrowSnapshot struct {
	Metadata            EscrowMetadata                `json:"metadata"`
	LatestNonce         uint64                        `json:"latest_nonce"`
	FeePerNonce         uint64                        `json:"fee_per_nonce,omitempty"`
	Retired             bool                          `json:"retired"`
	HostStats           map[uint32]types.HostStats    `json:"host_stats,omitempty"`
	SlotActivity        map[uint32]SlotActivity       `json:"slot_activity,omitempty"`
	Money               map[uint32]SlotMoney          `json:"money,omitempty"`
	Produced            map[uint32]uint64             `json:"produced_tokens,omitempty"`
	FirstContent        map[uint32]FirstContentTiming `json:"first_content,omitempty"`
	FirstContentBuckets FirstContentBuckets           `json:"first_content_buckets,omitempty"`
	Counters            []PersistedCounter            `json:"counters,omitempty"`
	Nonces              []PersistedNonce              `json:"nonces,omitempty"`
	ServiceNonces       []PersistedServiceCount       `json:"service_nonces,omitempty"`
	LiveCosts           []PersistedCost               `json:"live_costs,omitempty"`
	InputCounts         []PersistedInputCount         `json:"input_counts,omitempty"`
}

// PersistedCost is the money of one nonce the chain still holds; a sealed nonce lives in Money instead.
type PersistedCost struct {
	Nonce       uint64                `json:"nonce"`
	Reserved    uint64                `json:"reserved"`
	Actual      uint64                `json:"actual"`
	InputLength uint64                `json:"input_length"`
	MaxTokens   uint64                `json:"max_tokens"`
	Input       uint64                `json:"input"`
	Output      uint64                `json:"output"`
	Status      types.InferenceStatus `json:"status"`
}

// The gateway's side of the counts HostStats holds the chain's side of. See README.md, "Storage".
type SlotActivity struct {
	Challenged      uint64 `json:"challenged,omitempty"`
	Validations     uint64 `json:"validations,omitempty"`
	TimeoutsApplied uint64 `json:"timeouts_applied,omitempty"`
	Rejected        uint64 `json:"rejected,omitempty"`
}

type PersistedNonce struct {
	Nonce           uint64 `json:"nonce"`
	Sent            bool   `json:"sent"`
	Acknowledged    bool   `json:"acknowledged"`
	Usage           Usage  `json:"usage"`
	TimeoutKind     string `json:"timeout_kind,omitempty"`
	TimeoutAction   string `json:"timeout_action,omitempty"`
	TimeoutReason   string `json:"timeout_reason,omitempty"`
	Terminal        string `json:"terminal,omitempty"`
	Phase           Phase  `json:"phase,omitempty"`
	SlowReceipt     bool   `json:"slow_receipt,omitempty"`
	SlowChunk       bool   `json:"slow_chunk,omitempty"`
	ClockDrifted    bool   `json:"clock_drifted,omitempty"`
	SlowDecode      bool   `json:"slow_decode,omitempty"`
	LogprobsDecoded bool   `json:"logprobs_decoded,omitempty"`
}

type PersistedServiceCount struct {
	SlotID  uint32         `json:"slot_id"`
	Purpose ServicePurpose `json:"purpose"`
	Count   uint64         `json:"count"`
}

type PersistedCounter struct {
	CounterKey
	Count uint64 `json:"count"`
}

func (b *Book) Snapshot() Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     b.updatedAt,
		Escrows:       make([]EscrowSnapshot, 0, len(b.escrows)),
	}
	for _, escrowID := range slices.Sorted(maps.Keys(b.escrows)) {
		snapshot.Escrows = append(snapshot.Escrows, snapshotEscrow(b.escrows[escrowID]))
	}
	return snapshot
}

type unsavedChanges struct {
	snapshot         Snapshot
	escrowIDs        []string
	replaceStored    bool
	updatedAtUnsaved bool
}

func (changes unsavedChanges) empty() bool {
	return len(changes.escrowIDs) == 0 && !changes.replaceStored && !changes.updatedAtUnsaved
}

func (b *Book) takeUnsaved() unsavedChanges {
	changes := b.takeUnsavedEscrowIDs()
	for _, escrowID := range changes.escrowIDs {
		b.snapshotUnsaved(&changes, escrowID)
	}
	return changes
}

func (b *Book) takeUnsavedEscrowIDs() unsavedChanges {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.replaceStored {
		for escrowID := range b.escrows {
			b.unsaved[escrowID] = struct{}{}
		}
	}
	changes := unsavedChanges{
		snapshot:         Snapshot{SchemaVersion: SchemaVersion, UpdatedAt: b.updatedAt},
		escrowIDs:        slices.Sorted(maps.Keys(b.unsaved)),
		replaceStored:    b.replaceStored,
		updatedAtUnsaved: b.updatedAtUnsaved,
	}
	clear(b.unsaved)
	b.replaceStored, b.updatedAtUnsaved = false, false
	return changes
}

func (b *Book) snapshotUnsaved(changes *unsavedChanges, escrowID string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	changes.snapshot.UpdatedAt = b.updatedAt
	if escrow, known := b.escrows[escrowID]; known {
		changes.snapshot.Escrows = append(changes.snapshot.Escrows, snapshotEscrow(escrow))
	}
}

func (b *Book) requeueUnsaved(changes unsavedChanges) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, escrowID := range changes.escrowIDs {
		b.unsaved[escrowID] = struct{}{}
	}
	b.replaceStored = b.replaceStored || changes.replaceStored
	b.updatedAtUnsaved = b.updatedAtUnsaved || changes.updatedAtUnsaved
}

func (b *Book) replaceStoredOnNextSave() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.replaceStored = true
}

func snapshotEscrow(escrow *escrowLedger) EscrowSnapshot {
	stored := EscrowSnapshot{
		Metadata:    escrow.metadata,
		LatestNonce: escrow.latest,
		FeePerNonce: escrow.feePerNonce,
		Retired:     escrow.retired,
		HostStats:   make(map[uint32]types.HostStats, len(escrow.hostStats)),
		Counters:    make([]PersistedCounter, 0, len(escrow.counters)),
	}
	maps.Copy(stored.HostStats, escrow.hostStats)
	stored.SlotActivity = escrow.slotActivity()
	stored.Money = sealedMoney(escrow.folded)
	stored.LiveCosts = make([]PersistedCost, 0, len(escrow.costs))
	for nonce, cost := range escrow.costs {
		stored.LiveCosts = append(stored.LiveCosts, PersistedCost{
			Nonce: nonce, Reserved: cost.reserved, Actual: cost.actual, InputLength: cost.inputLength,
			MaxTokens: cost.maxTokens, Input: cost.input, Output: cost.output, Status: cost.status,
		})
	}
	slices.SortFunc(stored.LiveCosts, func(left, right PersistedCost) int { return cmp.Compare(left.Nonce, right.Nonce) })
	stored.InputCounts = escrow.persistedInputCounts()
	stored.Produced = maps.Clone(escrow.produced)
	stored.FirstContent = maps.Clone(escrow.firstContent)
	stored.FirstContentBuckets = maps.Clone(escrow.firstContentBuckets)
	for key, count := range escrow.serviceNonces {
		stored.ServiceNonces = append(stored.ServiceNonces, PersistedServiceCount{SlotID: key.slotID, Purpose: key.purpose, Count: count})
	}
	slices.SortFunc(stored.ServiceNonces, func(left, right PersistedServiceCount) int {
		return cmp.Or(cmp.Compare(left.SlotID, right.SlotID), cmp.Compare(left.Purpose, right.Purpose))
	})
	for key, count := range escrow.counters {
		stored.Counters = append(stored.Counters, PersistedCounter{CounterKey: key, Count: count})
	}
	for nonce, record := range escrow.nonces {
		if !revisable(record) {
			continue
		}
		stored.Nonces = append(stored.Nonces, PersistedNonce{
			Nonce:           nonce,
			Sent:            record.sent,
			Acknowledged:    record.acknowledged,
			Usage:           record.usage,
			TimeoutKind:     record.timeoutKind,
			TimeoutAction:   record.timeoutAction,
			TimeoutReason:   record.timeoutReason,
			Terminal:        record.terminal,
			Phase:           record.phase,
			SlowReceipt:     record.slowReceipt,
			SlowChunk:       record.slowChunk,
			ClockDrifted:    record.clockDrifted,
			SlowDecode:      record.slowDecode,
			LogprobsDecoded: record.logprobsDecoded,
		})
	}
	slices.SortFunc(stored.Nonces, func(left, right PersistedNonce) int {
		return cmp.Compare(left.Nonce, right.Nonce)
	})
	slices.SortFunc(stored.Counters, func(left, right PersistedCounter) int {
		return compareCounterKey(left.CounterKey, right.CounterKey)
	})
	return stored
}

// A snapshot from another schema is refused rather than half-read.
func (b *Book) Restore(snapshot Snapshot) error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("accounting: snapshot schema %d is not %d", snapshot.SchemaVersion, SchemaVersion)
	}
	restored := make(map[string]*escrowLedger, len(snapshot.Escrows))
	for _, stored := range snapshot.Escrows {
		if stored.Metadata.EscrowID == "" || len(stored.Metadata.Slots) == 0 {
			return fmt.Errorf("accounting: snapshot holds an escrow without id or slots")
		}
		escrow := newEscrowLedger(stored.Metadata)
		escrow.latest = stored.LatestNonce
		escrow.restoredLatest, escrow.restoredUnreplayed = stored.LatestNonce, true
		escrow.feePerNonce = stored.FeePerNonce
		for _, service := range stored.ServiceNonces {
			escrow.serviceNonces[serviceKey{slotID: service.SlotID, purpose: service.Purpose}] += service.Count
		}
		escrow.retired = stored.Retired
		maps.Copy(escrow.hostStats, stored.HostStats)
		maps.Copy(escrow.folded, stored.Money)
		for _, live := range stored.LiveCosts {
			escrow.costs[live.Nonce] = nonceCost{
				reserved: live.Reserved, actual: live.Actual, inputLength: live.InputLength,
				maxTokens: live.MaxTokens, input: live.Input, output: live.Output, status: live.Status,
			}
		}
		escrow.restoreInputCounts(stored.InputCounts)
		maps.Copy(escrow.produced, stored.Produced)
		maps.Copy(escrow.firstContent, stored.FirstContent)
		maps.Copy(escrow.firstContentBuckets, stored.FirstContentBuckets)
		for slotID, activity := range stored.SlotActivity {
			escrow.challenged[slotID] = activity.Challenged
			escrow.validations[slotID] = activity.Validations
			escrow.timeouts[slotID] = activity.TimeoutsApplied
			escrow.rejected[slotID] = activity.Rejected
		}
		for _, counter := range stored.Counters {
			escrow.counters[counter.CounterKey] += counter.Count
		}
		for _, stored := range stored.Nonces {
			record := &nonceRecord{
				raced:           true,
				sent:            stored.Sent,
				acknowledged:    stored.Acknowledged,
				usage:           stored.Usage,
				timeoutKind:     stored.TimeoutKind,
				timeoutAction:   stored.TimeoutAction,
				timeoutReason:   stored.TimeoutReason,
				terminal:        stored.Terminal,
				phase:           stored.Phase,
				slowReceipt:     stored.SlowReceipt,
				slowChunk:       stored.SlowChunk,
				clockDrifted:    stored.ClockDrifted,
				slowDecode:      stored.SlowDecode,
				logprobsDecoded: stored.LogprobsDecoded,
			}
			escrow.nonces[stored.Nonce] = record
			if key, settled := classify(escrow.slotOf(stored.Nonce), record); settled {
				record.countedAs, record.isCounted = key, true
				if record.timeoutAction != engine.TimeoutActionStarted {
					continue
				}
			}
			record.timeoutAction = engine.TimeoutActionAbandoned
			escrow.reclassify(stored.Nonce, record)
		}
		restored[stored.Metadata.EscrowID] = escrow
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.escrows = restored
	b.unsaved = make(map[string]struct{}, len(restored))
	for escrowID := range restored {
		b.unsaved[escrowID] = struct{}{}
	}
	b.updatedAt = snapshot.UpdatedAt
	return nil
}

// Whether a nonce's disposition can still move. See README.md, "Storage".
func revisable(record *nonceRecord) bool {
	if !record.isCounted {
		return true
	}
	return isUnfinishedDisposition(record.countedAs.Disposition)
}
