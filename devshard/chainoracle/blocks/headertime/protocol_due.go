package headertime

import (
	"math"
	"time"
)

// KindRefused and KindExecution are the timeout kinds Due echoes back.
// The caller picks the kind before the call. Due does not switch it.
const (
	KindRefused   = "refused"
	KindExecution = "execution"
)

// FeedPoll is how far ahead a header-mode deadline sits when the stamp is
// missing. The gateway wakes and reads the map again. Due does not sleep.
const FeedPoll = 250 * time.Millisecond

// ClockDueInput is one timeout check. The caller resolves the stamp and
// passes now. Due does not read the header map, sleep, or call the network.
//
// WallAnchor is Unix seconds. Zero means the caller has no wall instant.
// That is unarmed, not the Unix epoch, unless LegacyZeroIsEpoch is set.
// The verifier sets LegacyZeroIsEpoch because a signed StartedAt or
// ConfirmedAt of 0 is the epoch on the v6 check. The gateway does not:
// a missing sendTime falls back to the record anchor, and if that is also
// missing the challenge is unarmed.
//
// Extra is TimeoutBuffer, plus sweep grace when the caller is the sweep.
// Header mode ignores Extra. Interval is RefusalTimeout or ExecutionTimeout.
type ClockDueInput struct {
	HeightsyncTimeoutClock bool
	Reason                 string
	Now                    time.Time
	// Wall is the anchor instant when the caller has one, including
	// subseconds. The zero Time means "use WallAnchor". time.Unix(0, 0)
	// is set and is the epoch.
	Wall              time.Time
	WallAnchor        int64
	StampTimeMs       int64
	StampMissing      bool
	StampHeight       uint64
	Interval          time.Duration
	Extra             time.Duration
	Waited            time.Duration
	Caller            Caller
	LegacyZeroIsEpoch bool
}

// Due is the active timeout clock. Legacy deadline is WallAnchor + Interval
// + Extra, due when now is not strictly before that instant. Header follows
// Decide: a due stamp is placed 1ms in the past, a missing stamp stays
// FeedPoll ahead, and a past wall anchor does not open it.
//
// armed is false only for a legacy check with no wall anchor. The deadline
// is then the zero Time, which is not the Unix epoch. Callers must treat
// armed == false as not due. A zero Time is in year 1, so a bare
// !now.Before(deadline) would look past.
func Due(in ClockDueInput) (deadline time.Time, kind string, armed bool) {
	kind = in.Reason
	if kind != KindExecution {
		kind = KindRefused
	}
	if in.HeightsyncTimeoutClock {
		return headerDue(in), kind, true
	}
	if !in.Wall.IsZero() {
		return in.Wall.Add(saturateExtra(in.Interval, in.Extra)), kind, true
	}
	anchor := in.WallAnchor
	if anchor == 0 && in.LegacyZeroIsEpoch {
		return time.Unix(0, 0).Add(saturateExtra(in.Interval, in.Extra)), kind, true
	}
	if anchor <= 0 {
		return time.Time{}, kind, false
	}
	return time.Unix(anchor, 0).Add(saturateExtra(in.Interval, in.Extra)), kind, true
}

// headerDue is the stamp arm. Extra is not added. Decide's strict After
// stays: a stamp exactly at now-interval is not due.
func headerDue(in ClockDueInput) time.Time {
	d := Decide(in.Now, in.StampTimeMs, in.StampMissing, in.Interval, in.Waited, in.Caller)
	switch d.Outcome {
	case OutcomeDue:
		return in.Now.Add(-time.Millisecond)
	case OutcomeNotYet, OutcomeSchedule, OutcomeIgnore:
		if d.Delta <= 0 {
			return in.Now.Add(time.Millisecond)
		}
		return in.Now.Add(d.Delta)
	default:
		return in.Now.Add(FeedPoll)
	}
}

// saturateExtra adds extra to interval unless the sum would overflow a
// Duration. Header mode does not call this.
func saturateExtra(interval, extra time.Duration) time.Duration {
	if interval < 0 {
		interval = 0
	}
	if extra <= 0 {
		return interval
	}
	if interval > time.Duration(math.MaxInt64)-extra {
		return interval
	}
	return interval + extra
}
