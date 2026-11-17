package engine

import "sync"

const lookupRefusalsTrusted = 3

type lookupRefusals struct {
	mu          sync.Mutex
	consecutive map[hostModelKey]int
}

func newLookupRefusals() *lookupRefusals {
	return &lookupRefusals{consecutive: map[hostModelKey]int{}}
}

func (r *lookupRefusals) judge(attempt AttemptOutcome, model string) AttemptOutcome {
	if attempt.Terminal == TerminalUnclassified {
		return attempt
	}
	key := hostModelKey{participant: attempt.Participant, model: model}
	r.mu.Lock()
	defer r.mu.Unlock()
	if attempt.Terminal != TerminalEscrowLookupLimited {
		delete(r.consecutive, key)
		return attempt
	}
	r.consecutive[key]++
	if r.consecutive[key] > lookupRefusalsTrusted {
		attempt.Terminal = TerminalThrottled
	}
	return attempt
}
