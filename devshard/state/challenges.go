package state

import (
	"math"

	"devshard/types"
)

// Phase A lets any member open a challenge with one Valid:false, and every
// other member then owes a Phase-2 vote on it. Without a bound, one member can
// challenge all foreign work so that votes on real challenges never reach the
// threshold. An honest member challenges only work it sampled, so its open
// challenges stay near its sampled share of live records; the limit is a
// multiple of that plus a constant.
const (
	challengeLimitBase   = 16
	challengeLimitFactor = 2
)

// challengeLimitLocked is how many open challenges addr may hold:
// base + factor * rate * slots(addr) * live / (10000 * (total - maxSlots)),
// an upper bound on its expected sampled count over the live records.
func (sm *StateMachine) challengeLimitLocked(addr string) uint32 {
	rest := uint64(sm.totalSlots) - uint64(sm.maxAddressSlotsLocked())
	if rest == 0 {
		return math.MaxUint32
	}
	num := uint64(challengeLimitFactor) * uint64(sm.state.Config.ValidationRate) *
		uint64(sm.addressToSlotCount[addr]) * uint64(len(sm.state.Inferences))
	den := 10000 * rest
	limit := challengeLimitBase + (num+den-1)/den
	if limit > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(limit)
}

func (sm *StateMachine) maxAddressSlotsLocked() uint32 {
	var m uint32
	for _, n := range sm.addressToSlotCount {
		m = max(m, n)
	}
	return m
}

func (sm *StateMachine) addOpenChallengeLocked(addr string, delta int) {
	if j := sm.journal; j != nil {
		j.open.touch(sm.openChallenges, addr)
	}
	n := int(sm.openChallenges[addr]) + delta
	if n <= 0 {
		delete(sm.openChallenges, addr)
		return
	}
	sm.openChallenges[addr] = uint32(n)
}

// releaseChallengeLocked drops rec from its opener's count when rec leaves
// Challenged. Caller must call it before changing rec.Status.
func (sm *StateMachine) releaseChallengeLocked(rec *types.InferenceRecord) {
	if rec.Status != types.StatusChallenged || rec.Challenger == 0 {
		return
	}
	if addr, ok := sm.slotToAddress[rec.Challenger-1]; ok {
		sm.addOpenChallengeLocked(addr, -1)
	}
}

// rebuildOpenChallengesLocked recounts openChallenges from the live records.
func (sm *StateMachine) rebuildOpenChallengesLocked() {
	next := make(map[string]uint32)
	for _, rec := range sm.state.Inferences {
		if rec == nil || rec.Status != types.StatusChallenged || rec.Challenger == 0 {
			continue
		}
		if addr, ok := sm.slotToAddress[rec.Challenger-1]; ok {
			next[addr]++
		}
	}
	sm.openChallenges = next
}

// OpenChallenges returns how many live challenges addr has opened.
func (sm *StateMachine) OpenChallenges(addr string) uint32 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.openChallenges[addr]
}
