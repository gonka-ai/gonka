package main

import (
	"time"

	"devshard/accounting"
)

// executorStampTruncation is the second the executor's receipt is stamped in; it rounds down, so half of
// it is added back rather than read as a host running behind.
const executorStampTruncation = time.Second

// attemptTiming reports what this attempt measured. It only measures: which of it counts as a fault is
// the ledger's to decide, so no threshold is read here.
func attemptTiming(inf *inflight) accounting.AttemptTiming {
	timing := accounting.AttemptTiming{MaxChunkGap: time.Duration(inf.maxChunkGap.Load())}
	receiptAt := inf.receiptAt()
	if inf.sendTime.IsZero() || !receiptAt.After(inf.sendTime) {
		return timing
	}
	roundTrip := receiptAt.Sub(inf.sendTime)
	timing.Acknowledgement = roundTrip

	confirmedAt := int64(0)
	if inf.resp != nil {
		confirmedAt = inf.resp.ConfirmedAt
	}
	if confirmedAt == 0 {
		return timing
	}
	// The stamp landed somewhere inside the round trip, so it is compared against that window's
	// midpoint; comparing against the dispatch would charge the host for the outbound leg.
	midpoint := inf.sendTime.Add(roundTrip / 2)
	stamped := time.Unix(confirmedAt, 0).Add(executorStampTruncation / 2)
	timing.ClockOffset, timing.ClockMeasured = stamped.Sub(midpoint), true
	return timing
}
