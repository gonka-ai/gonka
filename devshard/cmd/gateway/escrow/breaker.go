package escrow

import (
	"sync"

	"devshard/cmd/gateway/store"
)

const maxCreateBreakerCooldownTicks = 4

type createBreakerState struct {
	failures      int
	cooldownTicks int
}

type createBreaker struct {
	mu     sync.Mutex
	states map[string]*createBreakerState
}

func newCreateBreaker() *createBreaker {
	return &createBreaker{states: make(map[string]*createBreakerState)}
}

func createBreakerKey(model string, role store.Role) string {
	return model + "|" + string(role)
}

// gated decrements an open cooldown by one tick per call; ticks are call counts, not durations.
func (b *createBreaker) gated(model string, role store.Role) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[createBreakerKey(model, role)]
	if !ok || state.cooldownTicks <= 0 {
		return false
	}
	state.cooldownTicks--
	return true
}

func (b *createBreaker) open(model string, role store.Role) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.states[createBreakerKey(model, role)]
	return ok && state.cooldownTicks > 0
}

func (b *createBreaker) recordFailure(model string, role store.Role) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := createBreakerKey(model, role)
	state, ok := b.states[key]
	if !ok {
		state = &createBreakerState{}
		b.states[key] = state
	}
	state.failures++
	state.cooldownTicks = escalatedCooldownTicks(state.failures)
}

func (b *createBreaker) reset(model string, role store.Role) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.states, createBreakerKey(model, role))
}

func escalatedCooldownTicks(failures int) int {
	cooldownTicks := 1
	for i := 1; i < failures && cooldownTicks < maxCreateBreakerCooldownTicks; i++ {
		cooldownTicks *= 2
	}
	if cooldownTicks > maxCreateBreakerCooldownTicks {
		cooldownTicks = maxCreateBreakerCooldownTicks
	}
	return cooldownTicks
}
