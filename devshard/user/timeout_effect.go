package user

import (
	"fmt"

	"devshard/types"
)

// timeoutTookEffect also accepts a record already timed out: a peer's diff may have carried the
// timeout before ours was composed.
func (s *Session) timeoutTookEffect(diff types.Diff, nonce uint64) bool {
	if HasMsgTimeout(diff.Txs, nonce) {
		return true
	}
	record, found := s.sm.GetInference(nonce)
	return found && record.Status == types.StatusTimedOut
}

// timeoutSettledError names what a sent diff did to the nonce. A diff the group carried without the
// timeout leaves the vote unposted, and the sentinel is what separates that from a settled one.
func timeoutSettledError(nonce uint64, reason types.TimeoutReason, applied bool) error {
	if applied {
		return fmt.Errorf("inference %d timed out: %s", nonce, reason)
	}
	return fmt.Errorf("inference %d timed out: %s: %w", nonce, reason, ErrTimeoutNotApplied)
}
