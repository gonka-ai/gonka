package accounting

import "devshard/cmd/gateway/engine"

type TimeoutOutcome string

// Where the engine's action-and-reason meets the ledger's single timeout outcome. See README.md.
func timeoutOutcomeOf(action, reason string) (TimeoutOutcome, bool) {
	switch action {
	case engine.TimeoutActionSkipped:
		return TimeoutSkipped, true
	case engine.TimeoutActionCompleted:
		return TimeoutApplied, true
	case engine.TimeoutActionFailed:
		switch reason {
		case engine.TimeoutReasonNotApplied:
			return TimeoutInsufficientVotes, true
		case engine.TimeoutReasonCollectionError, engine.TimeoutReasonEscrowGone:
			return TimeoutVoteCollectionFailed, true
		}
		return TimeoutVoteCollectionFailed, true
	}
	return TimeoutNoOutcome, false
}
