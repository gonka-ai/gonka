package accounting

import (
	"sort"
	"strings"
	"time"

	"devshard/types"
)

type participantKey struct {
	participant string
	model       string
}

// escrowView is a copy of one escrow's ledger. Query aggregates and sorts from
// these outside the lock, because a reader blocks a pending writer for as long
// as it holds it, and that writer is the per-diff observer on the sequencer's
// critical section.
type escrowView struct {
	id              string
	meta            EscrowMetadata
	latest          uint64
	counters        map[CounterKey]uint64
	hostStats       map[uint32]types.HostStats
	challengeBySlot map[uint32]uint64
	invalidBySlot   map[uint32]uint64
	live            []nonceState
}

// viewsFor copies the escrows a filter selects. This is the only part of a query
// that holds the read lock.
func (t *Tracker) viewsFor(filter QueryFilter) ([]escrowView, time.Time) {
	escrowFilter := stringSet(filter.EscrowIDs)
	t.mu.RLock()
	defer t.mu.RUnlock()
	views := make([]escrowView, 0, len(t.escrows))
	for escrowID, escrow := range t.escrows {
		if escrow.Meta.CreationEpoch != filter.EpochIndex {
			continue
		}
		if filter.Model != "" && escrow.Meta.Model != filter.Model {
			continue
		}
		if len(escrowFilter) > 0 {
			if _, ok := escrowFilter[escrowID]; !ok {
				continue
			}
		}
		views = append(views, escrow.view(escrowID))
	}
	return views, t.updated
}

// view copies everything a query reads. Meta.Slots is shared because it is
// copied once at registration and never mutated after. nonceState.Counted is
// only tested for nil, never dereferenced.
//
// The per-nonce sets are folded into per-slot totals here, on top of whatever
// the pre-set layout left behind, so the rest of the query path sees one shape
// regardless of which layout the escrow was loaded from.
func (e *escrowState) view(id string) escrowView {
	out := escrowView{
		id:              id,
		meta:            e.Meta,
		latest:          e.Latest,
		counters:        make(map[CounterKey]uint64, len(e.Counters)+len(e.ProtocolOnly)),
		hostStats:       make(map[uint32]types.HostStats, len(e.HostStats)),
		challengeBySlot: copyUint32Map(e.ChallengeBySlot),
		invalidBySlot:   copyUint32Map(e.InvalidBySlot),
		live:            make([]nonceState, 0, len(e.Live)),
	}
	for key, count := range e.Counters {
		out.counters[key] = count
	}
	for _, slot := range e.ProtocolOnly {
		out.counters[CounterKey{SlotID: slot, Disposition: DispositionProtocolOnly}]++
	}
	for _, rec := range e.Challenge {
		if !rec.Resolved {
			out.challengeBySlot[rec.Slot]++
		}
	}
	for _, slot := range e.Invalid {
		out.invalidBySlot[slot]++
	}
	for slot, stats := range e.HostStats {
		out.hostStats[slot] = stats
	}
	for _, state := range e.Live {
		out.live = append(out.live, *state)
	}
	return out
}

// Query mutates nothing and never stands between a committed diff and its
// counters. Live nonces past their deadline are classified for this response;
// the writer promotes them on the snapshot tick.
func (t *Tracker) Query(filter QueryFilter) []ParticipantRecord {
	views, updated := t.viewsFor(filter)
	now := t.nowUTC()
	records := make(map[participantKey]*ParticipantRecord)
	nonceSeen := make(map[participantKey]map[string]struct{})

	for i := range views {
		escrow := &views[i]
		escrowID := escrow.id
		for _, slot := range escrow.meta.Slots {
			if filter.Participant != "" && slot.ValidatorAddress != filter.Participant {
				continue
			}
			key := participantKey{participant: slot.ValidatorAddress, model: escrow.meta.Model}
			record := records[key]
			if record == nil {
				record = &ParticipantRecord{
					SchemaVersion:   SchemaVersion,
					UpdatedAt:       updated,
					EpochIndex:      filter.EpochIndex,
					Participant:     key.participant,
					Model:           key.model,
					Dispositions:    make(map[Disposition]uint64),
					TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
				}
				records[key] = record
				nonceSeen[key] = make(map[string]struct{})
			}
			if _, ok := nonceSeen[key][escrowID]; !ok {
				record.LatestNonces = append(record.LatestNonces, EscrowNonce{
					EscrowID:    escrowID,
					LatestNonce: escrow.latest,
					Phase:       escrow.meta.Phase,
				})
				nonceSeen[key][escrowID] = struct{}{}
			}
			slotRecord := buildSlotRecord(escrow, slot.SlotID, now)
			record.Slots = append(record.Slots, slotRecord)
			record.AssignedNonces += slotRecord.AssignedNonces
			record.ProtocolMisses += slotRecord.ProtocolMisses
			record.ProtocolInvalid += slotRecord.ProtocolInvalid
			record.UnresolvedChallenges += slotRecord.UnresolvedChallenges
			record.InFlight += slotRecord.InFlight
			record.TimeoutPending += slotRecord.TimeoutPending
			record.PendingClassification += slotRecord.PendingClassification
			record.Unclassified += slotRecord.Unclassified
			record.Overclassified += slotRecord.Overclassified
			record.UnknownReasonTotal += slotRecord.UnknownReasonTotal
			record.CrossChecks.TimeoutApplied += slotRecord.TimeoutOutcomes[TimeoutApplied]
			record.CrossChecks.HostMissed += slotRecord.ProtocolMisses
			record.CrossChecks.RecordedInvalid += slotRecord.RecordedInvalid
			record.CrossChecks.HostInvalid += slotRecord.ProtocolInvalid
			record.CrossChecks.ErrorCount += slotRecord.CrossCheckError
			for d, count := range slotRecord.Dispositions {
				record.Dispositions[d] += count
			}
			for outcome, count := range slotRecord.TimeoutOutcomes {
				record.TimeoutOutcomes[outcome] += count
			}
			counterTotals := make(map[CounterKey]uint64)
			for counterKey, count := range escrow.counters {
				if counterKey.SlotID != slot.SlotID {
					continue
				}
				counterTotals[counterKey] += count
			}
			for i := range escrow.live {
				live := &escrow.live[i]
				if live.SlotID != slot.SlotID || live.Counted != nil {
					continue
				}
				if counterKey, ok := live.counterKey(escrow.meta, now); ok {
					counterTotals[counterKey]++
				}
			}
			for _, counterKey := range sortedCounterKeys(counterTotals) {
				record.Counters = append(record.Counters, CounterRecord{
					EscrowID: escrowID,
					Key:      counterKey,
					Count:    counterTotals[counterKey],
				})
			}
		}
	}

	out := make([]ParticipantRecord, 0, len(records))
	for _, record := range records {
		sort.Slice(record.LatestNonces, func(i, j int) bool {
			return record.LatestNonces[i].EscrowID < record.LatestNonces[j].EscrowID
		})
		sort.Slice(record.Slots, func(i, j int) bool {
			if record.Slots[i].EscrowID == record.Slots[j].EscrowID {
				return record.Slots[i].SlotID < record.Slots[j].SlotID
			}
			return record.Slots[i].EscrowID < record.Slots[j].EscrowID
		})
		sort.Slice(record.Counters, func(i, j int) bool {
			if record.Counters[i].EscrowID == record.Counters[j].EscrowID {
				return counterSortKey(record.Counters[i].Key) < counterSortKey(record.Counters[j].Key)
			}
			return record.Counters[i].EscrowID < record.Counters[j].EscrowID
		})
		out = append(out, *record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Participant == out[j].Participant {
			return out[i].Model < out[j].Model
		}
		return out[i].Participant < out[j].Participant
	})
	return out
}

// addCounter folds one counter key into the slot record. A timeout is pending
// while its disposition is terminal but its outcome is not yet known; an unknown
// reason in any position makes the whole counter unknown.
func (r *SlotRecord) addCounter(key CounterKey, count uint64) {
	r.Dispositions[key.Disposition] += count
	if key.TimeoutOutcome != "" {
		r.TimeoutOutcomes[key.TimeoutOutcome] += count
	}
	if (key.Disposition == DispositionUnfinishedRefused ||
		key.Disposition == DispositionUnfinishedExecution) &&
		key.TimeoutOutcome == "" {
		r.TimeoutPending += count
	}
	if key.NoSendReason == NoSendUnknown || key.DetailReason == "unknown" || key.TimeoutReason == TimeoutReasonUnknown {
		r.UnknownReasonTotal += count
	}
}

func buildSlotRecord(escrow *escrowView, slot uint32, now time.Time) SlotRecord {
	assigned, _ := AssignedNoncesForSlot(escrow.latest, uint64(len(escrow.meta.Slots)), slot)
	record := SlotRecord{
		EscrowID:        escrow.id,
		SlotID:          slot,
		AssignedNonces:  assigned,
		Dispositions:    make(map[Disposition]uint64),
		TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
	}
	var accounted uint64
	for key, count := range escrow.counters {
		if key.SlotID != slot || count == 0 {
			continue
		}
		record.addCounter(key, count)
		accounted += count
	}
	// Unpromoted live nonces go through the same fold, so a nonce reads the same
	// before and after the writer promotes it.
	for i := range escrow.live {
		live := &escrow.live[i]
		if live.SlotID != slot {
			continue
		}
		switch {
		case live.Counted != nil:
			continue
		case live.Sent && !live.Finished:
			if key, ok := live.counterKey(escrow.meta, now); ok {
				record.addCounter(key, 1)
				accounted++
				continue
			}
			record.InFlight++
			accounted++
		default:
			record.PendingClassification++
			accounted++
		}
	}
	record.UnresolvedChallenges = escrow.challengeBySlot[slot]
	record.RecordedInvalid = escrow.invalidBySlot[slot]
	if stats, ok := escrow.hostStats[slot]; ok {
		record.ProtocolMisses = uint64(stats.Missed)
		record.ProtocolInvalid = uint64(stats.Invalid)
	}
	if accounted < assigned {
		record.Unclassified = assigned - accounted
	} else if accounted > assigned {
		record.Overclassified = accounted - assigned
	}
	// Differences are taken per slot of one escrow. Summing the two sides first
	// would let a surplus in one escrow hide a shortfall in another.
	record.CrossCheckError =
		absDiff(record.TimeoutOutcomes[TimeoutApplied], record.ProtocolMisses) +
			absDiff(record.RecordedInvalid, record.ProtocolInvalid) +
			record.Overclassified
	return record
}

func (t *Tracker) Epochs(filter QueryFilter) []EpochSummary {
	t.mu.RLock()
	epochs := make(map[uint64]struct{})
	escrowFilter := stringSet(filter.EscrowIDs)
	for escrowID, escrow := range t.escrows {
		if filter.Model != "" && escrow.Meta.Model != filter.Model {
			continue
		}
		if len(escrowFilter) > 0 {
			if _, ok := escrowFilter[escrowID]; !ok {
				continue
			}
		}
		epochs[escrow.Meta.CreationEpoch] = struct{}{}
	}
	t.mu.RUnlock()

	ids := make([]uint64, 0, len(epochs))
	for epoch := range epochs {
		ids = append(ids, epoch)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]EpochSummary, 0, len(ids))
	for _, epoch := range ids {
		records := t.Query(QueryFilter{
			EpochIndex: epoch,
			Model:      filter.Model,
			EscrowIDs:  filter.EscrowIDs,
		})
		summary := EpochSummary{
			SchemaVersion:   SchemaVersion,
			EpochIndex:      epoch,
			Dispositions:    make(map[Disposition]uint64),
			TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
		}
		for _, record := range records {
			if record.UpdatedAt.After(summary.UpdatedAt) {
				summary.UpdatedAt = record.UpdatedAt
			}
			summary.AssignedNonces += record.AssignedNonces
			summary.ProtocolMisses += record.ProtocolMisses
			summary.ProtocolInvalid += record.ProtocolInvalid
			summary.UnresolvedChallenges += record.UnresolvedChallenges
			summary.InFlight += record.InFlight
			summary.TimeoutPending += record.TimeoutPending
			summary.PendingClassification += record.PendingClassification
			summary.Unclassified += record.Unclassified
			summary.Overclassified += record.Overclassified
			summary.UnknownReasonTotal += record.UnknownReasonTotal
			summary.CrossCheckErrors += record.CrossChecks.ErrorCount
			for disposition, count := range record.Dispositions {
				summary.Dispositions[disposition] += count
			}
			for outcome, count := range record.TimeoutOutcomes {
				summary.TimeoutOutcomes[outcome] += count
			}
		}
		out = append(out, summary)
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out[item] = struct{}{}
			}
		}
	}
	return out
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
