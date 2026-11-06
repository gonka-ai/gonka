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
		if attempt.hasFirstToken() {
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
