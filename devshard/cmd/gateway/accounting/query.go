package accounting

import (
	"cmp"
	"maps"
	"slices"
	"strings"
)

// Participant and epoch totals are aggregated on read, never stored: a stored total could only ever disagree with its own parts.
func (b *Book) Query(filter QueryFilter) []ParticipantRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.query(b.admittedEscrows(filter), filter, true)
}

func (b *Book) query(admitted []string, filter QueryFilter, detailed bool) []ParticipantRecord {
	records := make(map[participantIdentity]*ParticipantRecord)
	for _, escrowID := range admitted {
		escrow := b.escrows[escrowID]
		for _, slot := range escrow.slots(escrowID) {
			if filter.Participant != "" && slot.Participant != filter.Participant {
				continue
			}
			identity := escrow.identityOf(slot.Participant)
			record, known := records[identity]
			if !known {
				record = &ParticipantRecord{
					SchemaVersion: SchemaVersion,
					UpdatedAt:     b.updatedAt,
					EpochIndex:    identity.epoch,
					Participant:   identity.participant,
					Model:         identity.model,
					nonceTotals:   nonceTotals{Dispositions: make(map[Disposition]uint64), ServiceNonces: make(map[ServicePurpose]uint64)},
				}
				records[identity] = record
			}
			record.absorb(slot)
			if !detailed {
				continue
			}
			record.Slots = append(record.Slots, slot)
			if last := len(record.LatestNonces); last == 0 || record.LatestNonces[last-1].EscrowID != escrowID {
				record.LatestNonces = append(record.LatestNonces, EscrowNonce{
					EscrowID:    escrowID,
					LatestNonce: escrow.latest,
					Retired:     escrow.retired,
				})
			}
		}
	}
	if detailed {
		distributeCounters(b.escrows, admitted, records)
	}

	aggregated := make([]ParticipantRecord, 0, len(records))
	for _, identity := range slices.SortedFunc(maps.Keys(records), compareParticipantIdentity) {
		record := records[identity]
		if detailed {
			sortCountersWithinEscrows(record.Counters)
			record.Findings = findingsFor(*record)
		}
		aggregated = append(aggregated, *record)
	}
	return aggregated
}

// admittedEscrows are the escrow ids the filter admits, in id order. The caller holds b.mu.
func (b *Book) admittedEscrows(filter QueryFilter) []string {
	admitted := make([]string, 0, len(b.escrows))
	for _, escrowID := range slices.Sorted(maps.Keys(b.escrows)) {
		if filter.admitsEscrow(escrowID, b.escrows[escrowID].metadata) {
			admitted = append(admitted, escrowID)
		}
	}
	return admitted
}

func distributeCounters(escrows map[string]*escrowLedger, admitted []string, records map[participantIdentity]*ParticipantRecord) {
	var owners []*ParticipantRecord
	for _, escrowID := range admitted {
		escrow := escrows[escrowID]
		owners = owners[:0]
		for slotID := range uint32(len(escrow.metadata.Slots)) {
			owners = append(owners, records[escrow.identityOf(escrow.participantOf(slotID))])
		}
		// A stored counter naming a slot the group no longer holds keeps the participant it always had: none.
		beyondGroup := records[escrow.identityOf("")]
		for key, count := range escrow.counters {
			owner := beyondGroup
			if int(key.SlotID) < len(owners) {
				owner = owners[key.SlotID]
			}
			if owner != nil {
				owner.Counters = append(owner.Counters, CounterRecord{EscrowID: escrowID, CounterKey: key, Count: count})
			}
		}
	}
}

func sortCountersWithinEscrows(counters []CounterRecord) {
	for start := 0; start < len(counters); {
		end := start + 1
		for end < len(counters) && counters[end].EscrowID == counters[start].EscrowID {
			end++
		}
		slices.SortFunc(counters[start:end], compareCounterRecord)
		start = end
	}
}

func (f QueryFilter) admitsEscrow(escrowID string, metadata EscrowMetadata) bool {
	if f.EpochIndex != 0 && metadata.CreationEpoch != f.EpochIndex {
		return false
	}
	if f.Model != "" && metadata.Model != f.Model {
		return false
	}
	return len(f.EscrowIDs) == 0 || slices.Contains(f.EscrowIDs, escrowID)
}

func (b *Book) Epochs(filter QueryFilter) []EpochSummary {
	records, firstContentByModel := b.readEpochs(filter)
	summaries := make(map[uint64]*EpochSummary)
	for _, record := range records {
		summary, known := summaries[record.EpochIndex]
		if !known {
			summary = &EpochSummary{
				SchemaVersion:       SchemaVersion,
				UpdatedAt:           record.UpdatedAt,
				EpochIndex:          record.EpochIndex,
				nonceTotals:         nonceTotals{Dispositions: make(map[Disposition]uint64), ServiceNonces: make(map[ServicePurpose]uint64)},
				FirstContentByModel: firstContentByModel[record.EpochIndex],
			}
			summaries[record.EpochIndex] = summary
		}
		summary.absorb(record)
	}

	ordered := make([]EpochSummary, 0, len(summaries))
	for _, epoch := range slices.Sorted(maps.Keys(summaries)) {
		ordered = append(ordered, *summaries[epoch])
	}
	return ordered
}

func (b *Book) readEpochs(filter QueryFilter) ([]ParticipantRecord, map[uint64][]ModelFirstContent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	admitted := b.admittedEscrows(filter)
	return b.query(admitted, filter, false), b.firstContentByModel(admitted)
}

func (s *EpochSummary) absorb(record ParticipantRecord) {
	s.Participants++
	s.nonceTotals.add(record.nonceTotals)
	s.hostActivity.add(record.hostActivity)
}

func (b *Book) EscrowIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return slices.Sorted(maps.Keys(b.escrows))
}

// Epoch is part of the key: a rotation leaves escrows of adjacent epochs live at once.
type participantIdentity struct {
	participant string
	model       string
	epoch       uint64
}

func compareParticipantIdentity(left, right participantIdentity) int {
	if left.epoch != right.epoch {
		return cmp.Compare(left.epoch, right.epoch)
	}
	if byParticipant := strings.Compare(left.participant, right.participant); byParticipant != 0 {
		return byParticipant
	}
	return strings.Compare(left.model, right.model)
}

func (e *escrowLedger) identityOf(participant string) participantIdentity {
	return participantIdentity{
		participant: participant,
		model:       e.metadata.Model,
		epoch:       e.metadata.CreationEpoch,
	}
}

// The order the report's rows come out in.
func compareCounterRecord(left, right CounterRecord) int {
	if byEscrow := strings.Compare(left.EscrowID, right.EscrowID); byEscrow != 0 {
		return byEscrow
	}
	return compareCounterKey(left.CounterKey, right.CounterKey)
}

func compareCounterKey(left, right CounterKey) int {
	if left.SlotID != right.SlotID {
		return cmp.Compare(left.SlotID, right.SlotID)
	}
	return strings.Compare(string(left.Disposition), string(right.Disposition))
}

func (r *ParticipantRecord) absorb(slot SlotRecord) {
	r.nonceTotals.add(slot.nonceTotals)
	r.hostActivity.add(slot.hostActivity)
	r.timeoutTally.add(slot.timeoutTally)
	r.CrossChecks.TimeoutApplied += slot.TimeoutOutcomes[TimeoutApplied]
	r.CrossChecks.HostMissed += uint64(slot.ChainMissed)
	r.CrossChecks.HostInvalid += uint64(slot.ChainInvalid)
	r.CrossChecks.RecordedInvalid += slot.rejected
	// Per slot of one escrow: summing both sides first lets a surplus in one hide a shortfall in another.
	r.CrossChecks.ErrorCount += absDiff(slot.TimeoutsApplied, uint64(slot.ChainMissed)) + slot.Overcounted
}

func (e *escrowLedger) participantOf(slotID uint32) string {
	if int(slotID) >= len(e.metadata.Slots) {
		return ""
	}
	return e.metadata.Slots[slotID].ValidatorAddress
}

// slotAggregate is one slot's share of an escrow, folded in one pass so the record below is a copy.
type slotAggregate struct {
	counted      uint64
	pending      uint64
	inFlight     uint64
	dispositions map[Disposition]uint64
	openRequests map[string]struct{}
	tally        timeoutTally
}

func (e *escrowLedger) slots(escrowID string) []SlotRecord {
	groupSize := uint32(len(e.metadata.Slots))
	aggregates := make([]slotAggregate, groupSize)
	for key, count := range e.counters {
		if key.SlotID >= groupSize {
			continue
		}
		aggregate := &aggregates[key.SlotID]
		aggregate.counted += count
		if aggregate.dispositions == nil {
			aggregate.dispositions = make(map[Disposition]uint64)
		}
		aggregate.dispositions[key.Disposition] += count
		aggregate.tally.fold(key, count)
	}
	money := e.foldMoney()
	for nonce, record := range e.nonces {
		aggregate := &aggregates[e.slotOf(nonce)]
		switch {
		case record.isCounted:
		case record.sent && !record.finished:
			aggregate.inFlight++
			if record.requestID != "" && record.terminal == "" {
				if aggregate.openRequests == nil {
					aggregate.openRequests = make(map[string]struct{})
				}
				aggregate.openRequests[record.requestID] = struct{}{}
			}
		default:
			aggregate.pending++
		}
	}

	serviceBySlot := e.serviceBySlot(groupSize)
	records := make([]SlotRecord, 0, groupSize)
	for slotID := range groupSize {
		aggregate := &aggregates[slotID]
		if aggregate.dispositions == nil {
			aggregate.dispositions = make(map[Disposition]uint64)
		}
		slot := SlotRecord{
			EscrowID:    escrowID,
			SlotID:      slotID,
			Participant: e.metadata.Slots[slotID].ValidatorAddress,
			rejected:    e.rejected[slotID],
			nonceTotals: nonceTotals{
				Assigned:           assignedForSlot(e.latest, groupSize, slotID),
				Dispositions:       aggregate.dispositions,
				Pending:            aggregate.pending,
				ReservedCost:       money[slotID].Reserved,
				ActualCost:         money[slotID].Actual,
				RefundedCost:       money[slotID].Refunded,
				CountedNonces:      money[slotID].CountedNonces,
				EstimatedInput:     money[slotID].EstimatedInput,
				EstimatedError:     money[slotID].EstimatedError,
				TokenizedInput:     money[slotID].TokenizedInput,
				TokenizedError:     money[slotID].TokenizedError,
				MaxTokens:          money[slotID].MaxTokens,
				InputTokens:        money[slotID].Input,
				OutputTokens:       e.produced[slotID],
				ServiceNonces:      serviceBySlot[slotID],
				ServiceFee:         serviceBySlot[slotID].fee(e.feePerNonce),
				FirstContentTiming: e.firstContent[slotID],
			},
			hostActivity: hostActivity{
				InFlight:             aggregate.inFlight,
				openRequests:         aggregate.openRequests,
				InFlightRequests:     uint64(len(aggregate.openRequests)),
				UnresolvedChallenges: e.challenged[slotID],
				ValidationsPerformed: e.validations[slotID],
				TimeoutsApplied:      e.timeouts[slotID],
			},
			timeoutTally: aggregate.tally,
		}
		if money[slotID].TokenizedShareUnknown {
			slot.TokenizedInput, slot.TokenizedError = 0, 0
			slot.TokenizedUnknownSlots++
		}
		if stats, observed := e.hostStats[slotID]; observed {
			slot.ChainMissed, slot.ChainInvalid = stats.Missed, stats.Invalid
			slot.ChainCost = stats.Cost
			slot.RequiredValidations, slot.CompletedValidations = stats.RequiredValidations, stats.CompletedValidations
		}
		accounted := aggregate.counted + slot.Pending + slot.InFlight + serviceBySlot[slotID].total()
		if accounted > slot.Assigned {
			slot.Overcounted = accounted - slot.Assigned
		} else {
			slot.Unobserved = slot.Assigned - accounted
		}
		records = append(records, slot)
	}
	return records
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
