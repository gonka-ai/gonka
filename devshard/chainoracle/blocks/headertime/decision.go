package headertime

import (
	"math"
	"time"
)

// AnswerWait is the verifier's only 60 second bound. A remaining timeout at
// or above it is ignored. A missing header time is waited on only until it
// elapses. Callers do not pass their own bound. The gateway does not use it:
// a gateway waits until the header time is due, however long that is.
const AnswerWait = 60 * time.Second

// Caller is who is evaluating the timeout. The two callers do not share the
// 60 second results.
type Caller uint8

const (
	CallerGateway Caller = iota
	CallerVerifier
)

// Outcome is the single result of Decide.
type Outcome uint8

const (
	// OutcomeDue means now - time(stamp) is past the timeout interval.
	OutcomeDue Outcome = iota
	// OutcomeNotYet is the gateway result when the map has the time and the
	// interval has not elapsed. Delta is the time left, and it may exceed
	// AnswerWait.
	OutcomeNotYet
	// OutcomeSchedule is the verifier result when the map has the time and
	// the time left is under AnswerWait. Delta is that remainder.
	OutcomeSchedule
	// OutcomeIgnore is the verifier result when the map has the time and the
	// time left is AnswerWait or more. Do not sign and do not hold the request.
	OutcomeIgnore
	// OutcomeMissing is the gateway result when the map has no time. The
	// gateway decides whether to log that its feed is behind.
	OutcomeMissing
	// OutcomeWaiting is the verifier result when the map has no time and the
	// waiter has been waiting under AnswerWait.
	OutcomeWaiting
	// OutcomeFeedBehind is the verifier result when the map has no time and
	// the waiter has already waited AnswerWait.
	OutcomeFeedBehind
)

func (o Outcome) String() string {
	switch o {
	case OutcomeDue:
		return "due"
	case OutcomeNotYet:
		return "not_yet"
	case OutcomeSchedule:
		return "schedule"
	case OutcomeIgnore:
		return "ignore"
	case OutcomeMissing:
		return "missing"
	case OutcomeWaiting:
		return "waiting"
	case OutcomeFeedBehind:
		return "feed_behind"
	default:
		return "unknown"
	}
}

// Decision is one answer from Decide. Delta is set for OutcomeNotYet and
// OutcomeSchedule. It is the time left until now is past time(stamp) by the
// timeout interval.
type Decision struct {
	Outcome Outcome
	Delta   time.Duration
}

// Decide reports whether a timeout is due from a wall clock and one header
// time. It does not read the map and it does not call out.
//
// stampTimeMs is the header time in milliseconds. missing means this process
// has not seen that block, and then stampTimeMs is ignored. interval is
// RefusalTimeout or ExecutionTimeout. waited is how long a verifier has
// already waited for a missing entry. The inequality is strict: a wall clock
// exactly at time(stamp)+interval is not due.
func Decide(now time.Time, stampTimeMs int64, missing bool, interval, waited time.Duration, caller Caller) Decision {
	if missing {
		if caller == CallerVerifier {
			if waited >= AnswerWait {
				return Decision{Outcome: OutcomeFeedBehind}
			}
			return Decision{Outcome: OutcomeWaiting}
		}
		return Decision{Outcome: OutcomeMissing}
	}

	elapsed := now.Sub(time.UnixMilli(stampTimeMs))
	if elapsed > interval {
		return Decision{Outcome: OutcomeDue}
	}
	delta := timeLeft(interval, elapsed)
	if caller == CallerVerifier {
		if delta >= AnswerWait {
			return Decision{Outcome: OutcomeIgnore}
		}
		return Decision{Outcome: OutcomeSchedule, Delta: delta}
	}
	return Decision{Outcome: OutcomeNotYet, Delta: delta}
}

// timeLeft is interval-elapsed. The caller has already rejected elapsed past
// the interval, so a non-negative interval yields a non-negative result.
// A stamp far enough ahead that the gap does not fit in a Duration saturates
// instead of wrapping into a short remainder.
func timeLeft(interval, elapsed time.Duration) time.Duration {
	if elapsed >= 0 {
		return interval - elapsed
	}
	if interval > math.MaxInt64+elapsed {
		return time.Duration(math.MaxInt64)
	}
	return interval - elapsed
}
