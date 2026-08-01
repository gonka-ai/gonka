package accounting

import (
	"sort"
	"strings"
)

type participantKey struct {
	participant string
	model       string
}

func (b *Book) Query(filter QueryFilter) []ParticipantRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.queryLocked(filter)
}

func (b *Book) queryLocked(filter QueryFilter) []ParticipantRecord {
	escrowFilter := makeStringSet(filter.EscrowIDs)
	records := make(map[participantKey]*ParticipantRecord)
	nonceAdded := make(map[participantKey]map[string]struct{})

	for escrowID, escrow := range b.escrows {
		if escrow.metadata.CreationEpoch != filter.EpochIndex {
			continue
		}
		if filter.Model != "" && escrow.metadata.Model != filter.Model {
			continue
		}
		if len(escrowFilter) > 0 {
			if _, ok := escrowFilter[escrowID]; !ok {
				continue
			}
		}
		for _, assignment := range escrow.metadata.Slots {
			if filter.Participant != "" && assignment.ValidatorAddress != filter.Participant {
				continue
			}
			key := participantKey{participant: assignment.ValidatorAddress, model: escrow.metadata.Model}
			record := records[key]
			if record == nil {
				record = &ParticipantRecord{
					SchemaVersion:   SchemaVersion,
					UpdatedAt:       b.updatedAt,
					EpochIndex:      filter.EpochIndex,
					Participant:     assignment.ValidatorAddress,
					Model:           escrow.metadata.Model,
					Dispositions:    make(map[Disposition]uint64),
					TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
					RecordingErrors: b.eventErrors,
					WriterErrors:    b.writerErrors,
				}
				records[key] = record
				nonceAdded[key] = make(map[string]struct{})
			}
			if _, exists := nonceAdded[key][escrowID]; !exists {
				record.LatestNonces = append(record.LatestNonces, EscrowNonce{
					EscrowID:    escrowID,
					LatestNonce: escrow.latest,
					Phase:       escrow.metadata.Phase,
				})
				nonceAdded[key][escrowID] = struct{}{}
			}

			slot := buildSlotRecord(escrowID, escrow, assignment.SlotID)
			record.Slots = append(record.Slots, slot)
			record.AssignedNonces += slot.AssignedNonces
			record.ProtocolMisses += slot.ProtocolMisses
			record.ProtocolInvalid += slot.ProtocolInvalid
			record.UnresolvedChallenges += slot.UnresolvedChallenges
			record.InFlight += slot.InFlight
			record.PendingClassification += slot.PendingClassification
			record.Unclassified += slot.Unclassified
			record.Overclassified += slot.Overclassified
			record.UnknownReasonTotal += slot.UnknownReasonTotal
			record.CrossChecks.TimeoutApplied += slot.TimeoutOutcomes[TimeoutApplied]
			record.CrossChecks.HostMissed += slot.ProtocolMisses
			record.CrossChecks.RecordedInvalid += slot.RecordedInvalid
			record.CrossChecks.HostInvalid += slot.ProtocolInvalid
			record.CrossChecks.ErrorCount +=
				absoluteDifference(slot.TimeoutOutcomes[TimeoutApplied], slot.ProtocolMisses) +
					absoluteDifference(slot.RecordedInvalid, slot.ProtocolInvalid) +
					slot.Overclassified
			for disposition, count := range slot.Dispositions {
				record.Dispositions[disposition] += count
			}
			for outcome, count := range slot.TimeoutOutcomes {
				record.TimeoutOutcomes[outcome] += count
			}
			for counterKey, count := range escrow.counters {
				if counterKey.SlotID != assignment.SlotID || count == 0 {
					continue
				}
				record.Counters = append(record.Counters, CounterRecord{
					EscrowID:   escrowID,
					CounterKey: counterKey,
					Count:      count,
				})
			}
		}
	}

	result := make([]ParticipantRecord, 0, len(records))
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
			return counterRecordLess(record.Counters[i], record.Counters[j])
		})
		result = append(result, *record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Participant == result[j].Participant {
			return result[i].Model < result[j].Model
		}
		return result[i].Participant < result[j].Participant
	})
	return result
}

func buildSlotRecord(escrowID string, escrow *escrowBook, slotID uint32) SlotRecord {
	assigned, _ := AssignedNoncesForSlot(escrow.latest, uint64(len(escrow.metadata.Slots)), slotID)
	slot := SlotRecord{
		EscrowID:        escrowID,
		SlotID:          slotID,
		AssignedNonces:  assigned,
		Dispositions:    make(map[Disposition]uint64),
		TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
	}
	var classified uint64
	for key, count := range escrow.counters {
		if key.SlotID != slotID || count == 0 {
			continue
		}
		slot.Dispositions[key.Disposition] += count
		classified += count
		if key.TimeoutOutcome != "" {
			slot.TimeoutOutcomes[key.TimeoutOutcome] += count
		}
		if counterHasUnknownReason(key) {
			slot.UnknownReasonTotal += count
		}
	}
	for _, challengedSlot := range escrow.challenges {
		if challengedSlot == slotID {
			slot.UnresolvedChallenges++
		}
	}
	slot.RecordedInvalid = escrow.invalidBySlot[slotID]
	for _, state := range escrow.live {
		if state.slotID != slotID {
			continue
		}
		if state.sent && !state.finish && !state.timeoutRequired {
			slot.InFlight++
		} else if state.counted == nil {
			slot.PendingClassification++
		}
	}
	accounted := classified + slot.InFlight + slot.PendingClassification
	if accounted < slot.AssignedNonces {
		slot.Unclassified = slot.AssignedNonces - accounted
	} else if accounted > slot.AssignedNonces {
		slot.Overclassified = accounted - slot.AssignedNonces
	}
	if stats, ok := escrow.hostStats[slotID]; ok {
		slot.ProtocolMisses = uint64(stats.Missed)
		slot.ProtocolInvalid = uint64(stats.Invalid)
	}
	return slot
}

// counterHasUnknownReason reports whether a classified nonce fell back to an
// unknown value on a dimension that is expected to name a cause.
// failure_origin=transport_unknown is not such a fallback: it is one of the
// four fixed origins and stays visible as its own dimension.
func counterHasUnknownReason(key CounterKey) bool {
	return key.NoSendReason == NoSendUnknown ||
		key.DetailReason == "unknown" ||
		key.TimeoutReason == TimeoutReasonUnknown
}

func (b *Book) Epochs(filter QueryFilter) []EpochSummary {
	b.mu.RLock()
	defer b.mu.RUnlock()
	recordingErrors := b.eventErrors
	writerErrors := b.writerErrors
	epochSet := make(map[uint64]struct{})
	escrowFilter := makeStringSet(filter.EscrowIDs)
	for escrowID, escrow := range b.escrows {
		if filter.Model != "" && escrow.metadata.Model != filter.Model {
			continue
		}
		if len(escrowFilter) > 0 {
			if _, ok := escrowFilter[escrowID]; !ok {
				continue
			}
		}
		epochSet[escrow.metadata.CreationEpoch] = struct{}{}
	}
	epochs := make([]uint64, 0, len(epochSet))
	for epoch := range epochSet {
		epochs = append(epochs, epoch)
	}
	sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })

	result := make([]EpochSummary, 0, len(epochs))
	for _, epoch := range epochs {
		records := b.queryLocked(QueryFilter{
			EpochIndex: epoch,
			Model:      filter.Model,
			EscrowIDs:  filter.EscrowIDs,
		})
		summary := EpochSummary{
			SchemaVersion:   SchemaVersion,
			EpochIndex:      epoch,
			Dispositions:    make(map[Disposition]uint64),
			TimeoutOutcomes: make(map[TimeoutOutcome]uint64),
			RecordingErrors: recordingErrors,
			WriterErrors:    writerErrors,
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
			summary.CrossCheckErrors += record.CrossChecks.ErrorCount
			for disposition, count := range record.Dispositions {
				summary.Dispositions[disposition] += count
			}
			for outcome, count := range record.TimeoutOutcomes {
				summary.TimeoutOutcomes[outcome] += count
			}
		}
		result = append(result, summary)
	}
	return result
}

func makeStringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				result[item] = struct{}{}
			}
		}
	}
	return result
}

func absoluteDifference(left, right uint64) uint64 {
	if left > right {
		return left - right
	}
	return right - left
}

func counterRecordLess(left, right CounterRecord) bool {
	if left.EscrowID != right.EscrowID {
		return left.EscrowID < right.EscrowID
	}
	if left.SlotID != right.SlotID {
		return left.SlotID < right.SlotID
	}
	return counterSortKey(left.CounterKey) < counterSortKey(right.CounterKey)
}

func counterSortKey(key CounterKey) string {
	return strings.Join([]string{
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
