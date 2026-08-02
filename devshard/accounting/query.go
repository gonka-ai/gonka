package accounting

import (
	"sort"
	"strings"
)

type participantKey struct {
	participant string
	model       string
}

func (t *Tracker) Query(filter QueryFilter) []ParticipantRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	escrowFilter := stringSet(filter.EscrowIDs)
	records := make(map[participantKey]*ParticipantRecord)
	nonceSeen := make(map[participantKey]map[string]struct{})

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
		for _, slot := range escrow.Meta.Slots {
			if filter.Participant != "" && slot.ValidatorAddress != filter.Participant {
				continue
			}
			key := participantKey{participant: slot.ValidatorAddress, model: escrow.Meta.Model}
			record := records[key]
			if record == nil {
				record = &ParticipantRecord{
					SchemaVersion:   SchemaVersion,
					UpdatedAt:       t.updated,
					EpochIndex:      filter.EpochIndex,
					Participant:     key.participant,
					Model:           key.model,
					Dispositions:    make(map[Disposition]uint64),
					TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
					RecordingErrors: t.errCount,
					WriterErrors:    t.wrCount,
				}
				records[key] = record
				nonceSeen[key] = make(map[string]struct{})
			}
			if _, ok := nonceSeen[key][escrowID]; !ok {
				record.LatestNonces = append(record.LatestNonces, EscrowNonce{EscrowID: escrowID, LatestNonce: escrow.Latest})
				nonceSeen[key][escrowID] = struct{}{}
			}
			slotRecord := buildSlotRecord(escrowID, escrow, slot.SlotID)
			record.Slots = append(record.Slots, slotRecord)
			record.AssignedNonces += slotRecord.AssignedNonces
			record.ProtocolMisses += slotRecord.ProtocolMisses
			record.ProtocolInvalid += slotRecord.ProtocolInvalid
			record.UnresolvedChallenges += slotRecord.UnresolvedChallenges
			record.InFlight += slotRecord.InFlight
			record.PendingClassification += slotRecord.PendingClassification
			record.Unclassified += slotRecord.Unclassified
			record.Overclassified += slotRecord.Overclassified
			record.UnknownReasonTotal += slotRecord.UnknownReasonTotal
			record.CrossChecks.TimeoutApplied += slotRecord.TimeoutOutcomes[TimeoutApplied]
			record.CrossChecks.HostMissed += slotRecord.ProtocolMisses
			record.CrossChecks.RecordedInvalid += slotRecord.RecordedInvalid
			record.CrossChecks.HostInvalid += slotRecord.ProtocolInvalid
			for d, count := range slotRecord.Dispositions {
				record.Dispositions[d] += count
			}
			for outcome, count := range slotRecord.TimeoutOutcomes {
				record.TimeoutOutcomes[outcome] += count
			}
			for _, counterKey := range sortedCounterKeys(escrow.Counters) {
				if counterKey.SlotID != slot.SlotID {
					continue
				}
				record.Counters = append(record.Counters, CounterRecord{
					EscrowID: escrowID,
					Key:      counterKey,
					Count:    escrow.Counters[counterKey],
				})
			}
		}
	}

	out := make([]ParticipantRecord, 0, len(records))
	for _, record := range records {
		record.CrossChecks.ErrorCount =
			absDiff(record.CrossChecks.TimeoutApplied, record.CrossChecks.HostMissed) +
				absDiff(record.CrossChecks.RecordedInvalid, record.CrossChecks.HostInvalid)
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

func buildSlotRecord(escrowID string, escrow *escrowState, slot uint32) SlotRecord {
	assigned, _ := AssignedNoncesForSlot(escrow.Latest, uint64(len(escrow.Meta.Slots)), slot)
	record := SlotRecord{
		EscrowID:        escrowID,
		SlotID:          slot,
		AssignedNonces:  assigned,
		Dispositions:    make(map[Disposition]uint64),
		TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
	}
	var accounted uint64
	for key, count := range escrow.Counters {
		if key.SlotID != slot || count == 0 {
			continue
		}
		record.Dispositions[key.Disposition] += count
		accounted += count
		if key.TimeoutOutcome != "" {
			record.TimeoutOutcomes[key.TimeoutOutcome] += count
		}
		if key.NoSendReason == NoSendUnknown || key.DetailReason == "unknown" || key.TimeoutReason == TimeoutReasonUnknown {
			record.UnknownReasonTotal += count
		}
	}
	for _, live := range escrow.Live {
		if live.SlotID != slot {
			continue
		}
		switch {
		case live.Sent && !live.Finished && live.TimeoutOutcome == "":
			record.InFlight++
			accounted++
		case live.Counted == nil:
			record.PendingClassification++
			accounted++
		}
	}
	record.UnresolvedChallenges = escrow.ChallengeBySlot[slot]
	record.RecordedInvalid = escrow.InvalidBySlot[slot]
	if stats, ok := escrow.HostStats[slot]; ok {
		record.ProtocolMisses = uint64(stats.Missed)
		record.ProtocolInvalid = uint64(stats.Invalid)
	}
	if accounted < assigned {
		record.Unclassified = assigned - accounted
	} else if accounted > assigned {
		record.Overclassified = accounted - assigned
	}
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
			summary.PendingClassification += record.PendingClassification
			summary.Unclassified += record.Unclassified
			summary.Overclassified += record.Overclassified
			summary.UnknownReasonTotal += record.UnknownReasonTotal
			summary.RecordingErrors = record.RecordingErrors
			summary.WriterErrors = record.WriterErrors
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
