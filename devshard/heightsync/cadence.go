package heightsync

// ComputeCadenceSwallow implements periodic cadence tail suppression when a forced
// sync turn ends strictly inside a periodic Anchor window (Scenario C in SCENARIOS.md).
// Returns swallowUntil = pEnd and swallowFe = forcedEnd when suppression applies.
func ComputeCadenceSwallow(forcedStart, forcedEnd, anchorK, slotsNum uint64) (swallowUntil, swallowFe uint64, ok bool) {
	if anchorK == 0 || slotsNum == 0 || anchorK < slotsNum {
		return 0, 0, false
	}
	// Periodic windows i>=1: [i*K, i*K + slotsNum - 1]
	for i := uint64(1); ; i++ {
		pStart := i * anchorK
		pEnd := pStart + slotsNum - 1
		if pStart > forcedEnd+anchorK {
			break
		}
		if forcedStart <= pEnd && forcedEnd >= pStart {
			if forcedEnd > pStart && forcedEnd < pEnd {
				return pEnd, forcedEnd, true
			}
		}
	}
	return 0, 0, false
}

// EscrowHeightSyncHints carries escrow state into AnchorScheduler.Decide (non-JSON).
type EscrowHeightSyncHints struct {
	ForcedStart, ForcedEnd         uint64
	CadenceSwallowUntil, SwallowFe uint64
	TurnK, TurnSlots               uint64
}
