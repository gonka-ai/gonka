package perf

import "time"

func (t *Tracker) rememberRungsLocked() {
	t.rungMemory = make(map[hostKey]ejectionState)
	for key, host := range t.hosts {
		if host.ejection.ejectionCount > 0 {
			t.rungMemory[key] = host.ejection
		}
	}
}

func (t *Tracker) resumeRungLocked(key hostKey, state *ejectionState, policy ejectionPolicy, now time.Time) {
	remembered, held := t.rungMemory[key]
	if !held {
		return
	}
	delete(t.rungMemory, key)
	if !t.isUnvoteable(key.model, key.participant) {
		return
	}
	remembered.decayEjectionCount(policy.max, now)
	policy.eject(state, remembered.ejectionCount+1, now)
}
