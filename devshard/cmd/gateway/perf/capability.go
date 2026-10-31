package perf

import "sync"

// capabilityTracker counts refusals rather than judging them. See README.md, "What a capability refusal is keyed on".
type capabilityTracker struct {
	mu sync.RWMutex

	contextLimits   map[hostKey]uint64
	versionRefusals map[string]uint64
	toolRefusals    map[hostKey]uint64
	contextRefusals map[hostKey]uint64
	outputOverruns  map[hostKey]uint64
}

func newCapabilityTracker() *capabilityTracker {
	return &capabilityTracker{
		contextLimits:   make(map[hostKey]uint64),
		versionRefusals: make(map[string]uint64),
		toolRefusals:    make(map[hostKey]uint64),
		contextRefusals: make(map[hostKey]uint64),
		outputOverruns:  make(map[hostKey]uint64),
	}
}

// The three recorders report a first observation, so a caller can log it once and outside the lock.
func (c *capabilityTracker) recordContextLimit(participant, model string, maxTokens uint64) (previous uint64, changed bool) {
	if maxTokens == 0 {
		return 0, false
	}
	served := hostKey{participant: participant, model: model}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous = c.contextLimits[served]
	c.contextRefusals[served]++
	// The smallest refusal is the bound that holds: a later, larger one does not lift it.
	if previous != 0 && previous <= maxTokens {
		return previous, false
	}
	c.contextLimits[served] = maxTokens
	return previous, true
}

func (c *capabilityTracker) recordToolUnsupported(participant, model string) (first bool) {
	served := hostKey{participant: participant, model: model}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolRefusals[served]++
	return c.toolRefusals[served] == 1
}

func (c *capabilityTracker) recordVersionUnsupported(participant string) (first bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versionRefusals[participant]++
	return c.versionRefusals[participant] == 1
}

func (c *capabilityTracker) recordOutputOverrun(participant, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputOverruns[hostKey{participant: participant, model: model}]++
}

func (c *capabilityTracker) overruns(participant, model string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.outputOverruns[hostKey{participant: participant, model: model}]
}

// capability reports the refusal counts and the smallest context the host has admitted to.
func (c *capabilityTracker) capability(participant, model string) (contextLimit, versionRefusals, toolRefusals, contextRefusals uint64) {
	served := hostKey{participant: participant, model: model}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.contextLimits[served], c.versionRefusals[participant], c.toolRefusals[served], c.contextRefusals[served]
}
