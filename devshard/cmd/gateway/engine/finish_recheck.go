package engine

import "time"

const finishRecheckInterval = 10 * time.Second

func (r *raceRegistration) finishStates(steps []TimeoutStep) (waiting, applied int) {
	r.mu.Lock()
	target := r.target
	r.mu.Unlock()
	if target == nil {
		return 0, 0
	}
	for _, step := range steps {
		if !step.Post || step.Kind != SettleByVote {
			continue
		}
		switch {
		case target.NonceCanonicalFinished(step.Nonce):
			applied++
		case target.NonceFinished(step.Nonce):
			waiting++
		}
	}
	return waiting, applied
}

func (e *Engine) finishRechecked(steps []TimeoutStep, registration *raceRegistration, due func() time.Time) func() time.Time {
	return func() time.Time {
		waiting, applied := registration.finishStates(steps)
		switch {
		case waiting > 0:
			recheck := e.settles.now().Add(finishRecheckInterval)
			if deadline := due(); deadline.Before(recheck) {
				return deadline
			}
			return recheck
		case applied == len(steps):
			return postAtOnce
		}
		return due()
	}
}
