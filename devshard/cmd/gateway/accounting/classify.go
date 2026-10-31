package accounting

import "devshard/cmd/gateway/engine"

// A burn's record carries the gateway's own prompt and reserve. See docs/accounting.md, "Money and tokens".
func (e *escrowLedger) isGhost(nonce uint64) bool {
	record, known := e.nonces[nonce]
	return known && record.ghostReason != ""
}

func (e *escrowLedger) reclassify(nonce uint64, record *nonceRecord) {
	key, settled := classify(e.slotOf(nonce), record)
	if record.isCounted {
		if settled && record.countedAs == key {
			return
		}
		e.counters[record.countedAs]--
		if e.counters[record.countedAs] == 0 {
			delete(e.counters, record.countedAs)
		}
		record.isCounted = false
	}
	if !settled {
		return
	}
	e.counters[key]++
	record.countedAs, record.isCounted = key, true
}

func classify(slotID uint32, record *nonceRecord) (CounterKey, bool) {
	key := CounterKey{SlotID: slotID}
	switch {
	case record.ghostReason != "":
		key.Disposition = DispositionGhost
		key.GhostReason = record.ghostReason
		key.TimeoutKind = record.timeoutKind
		key.TimeoutAction = record.timeoutAction
		key.TimeoutReason = record.timeoutReason
		return key, true
	case record.finished:
		key.Disposition = finishedDisposition(record.usage)
		return raceFacts(key, record), true
	case record.timeoutAction == "":
		return key, false
	case !record.sent:
		key.Disposition = DispositionUnfinishedRefused
	default:
		key.Disposition = unfinishedDisposition(record)
	}
	key.TimeoutKind = record.timeoutKind
	key.TimeoutAction = record.timeoutAction
	key.TimeoutReason = record.timeoutReason
	return raceFacts(key, record), true
}

// An empty terminal is itself a fact, so it is named rather than left blank. See README.md.
func raceFacts(key CounterKey, record *nonceRecord) CounterKey {
	key.Terminal = record.terminal
	if key.Terminal == "" {
		key.Terminal = TerminalUnreported
	}
	key.Phase = record.phase
	key.SlowReceipt = record.slowReceipt
	key.SlowChunk = record.slowChunk
	key.ClockDrifted = record.clockDrifted
	key.SlowDecode = record.slowDecode
	key.LogprobsDecoded = record.logprobsDecoded
	return key
}

func finishedDisposition(usage Usage) Disposition {
	switch usage {
	case UsageWinner:
		return DispositionFinishedUsed
	case UsageLoser:
		return DispositionFinishedUnused
	default:
		return DispositionFinishedUsageUnknown
	}
}

// isUnfinishedDisposition is the family unfinishedDisposition can ever return.
func isUnfinishedDisposition(disposition Disposition) bool {
	return disposition == DispositionUnfinishedRefused || disposition == DispositionUnfinishedExecution
}

func unfinishedDisposition(record *nonceRecord) Disposition {
	switch {
	case record.timeoutKind == engine.TimeoutKindRefused:
		return DispositionUnfinishedRefused
	case record.timeoutKind != "":
		return DispositionUnfinishedExecution
	case record.acknowledged:
		return DispositionUnfinishedExecution
	default:
		return DispositionUnfinishedRefused
	}
}

func assignedForSlot(latest uint64, groupSize, slotID uint32) uint64 {
	if groupSize == 0 || latest == 0 {
		return 0
	}
	group, slot := uint64(groupSize), uint64(slotID)
	if slot == 0 {
		return latest / group
	}
	if latest < slot {
		return 0
	}
	return (latest-slot)/group + 1
}

func (e *escrowLedger) slotActivity() map[uint32]SlotActivity {
	activity := make(map[uint32]SlotActivity, len(e.metadata.Slots))
	record := func(slotID uint32, apply func(*SlotActivity)) {
		entry := activity[slotID]
		apply(&entry)
		activity[slotID] = entry
	}
	for slotID, count := range e.challenged {
		record(slotID, func(entry *SlotActivity) { entry.Challenged = count })
	}
	for slotID, count := range e.validations {
		record(slotID, func(entry *SlotActivity) { entry.Validations = count })
	}
	for slotID, count := range e.timeouts {
		record(slotID, func(entry *SlotActivity) { entry.TimeoutsApplied = count })
	}
	for slotID, count := range e.rejected {
		record(slotID, func(entry *SlotActivity) { entry.Rejected = count })
	}
	return activity
}
