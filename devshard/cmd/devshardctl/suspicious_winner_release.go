package main

func suspiciousWinnerRelease(attempts []*inflight) (*inflight, bool) {
	var finished []*inflight
	rivals := 0
	for _, attempt := range attempts {
		if inflightDone(attempt) {
			if inflightFinished(attempt) {
				finished = append(finished, attempt)
			}
			continue
		}
		if attempt.probe {
			continue
		}
		// Only a non-suspicious rival that has started streaming can crown itself
		// ahead of a deferred suspicious answer; another suspicious rival would
		// also defer and otherwise trigger fail-closed deadlock when all hosts
		// are on probation.
		if !attempt.suspicious && attempt.hasFirstToken() {
			return nil, false
		}
		rivals++
	}
	if rivals == 0 {
		return nil, false
	}
	fallback := fallbackSuspiciousWinner(finished)
	return fallback, fallback != nil
}
