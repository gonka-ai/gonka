package app

import (
	"sync"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

type windowReset interface {
	ResetAll() (pairs, cutOff int)
}

type hostReset interface {
	ResetHostStats() (withheld int)
}

type lookupReset interface {
	ResetLookupRefusals()
}

type hostStatsResetNarrator interface {
	HostStatsReset(epoch uint64, pairs, withheld, cutOff int)
}

type epochBoundaryReset struct {
	mu             sync.Mutex
	epoch          uint64
	repriceWindows func(chain.PhaseSnapshot)
	windows        windowReset
	hosts          hostReset
	lookups        lookupReset
	narrator       hostStatsResetNarrator
}

func (r *epochBoundaryReset) observe(snapshot chain.PhaseSnapshot) {
	r.repriceWindows(snapshot)
	if !r.crossed(snapshot.EffectiveEpochIndex) {
		return
	}
	windowPairs, cutOff := r.windows.ResetAll()
	withheld := r.hosts.ResetHostStats()
	r.lookups.ResetLookupRefusals()
	r.narrator.HostStatsReset(snapshot.EffectiveEpochIndex, windowPairs, withheld, cutOff)
}

func (r *epochBoundaryReset) crossed(epoch uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if epoch == chain.EpochIndexUnknown || epoch <= r.epoch {
		return false
	}
	previous := r.epoch
	r.epoch = epoch
	return previous != chain.EpochIndexUnknown
}

func subscribeEpochBoundaryReset(observer *chain.PhaseObserver, participants *limits.ParticipantLimiter, hosts *perf.Tracker, races *engine.Engine, events *journal.Journal) {
	observer.Subscribe((&epochBoundaryReset{
		repriceWindows: func(snapshot chain.PhaseSnapshot) {
			participants.ObserveModels(contextWindowsOf(snapshot))
			participants.ObserveWeights(participantWeightsOf(snapshot))
		},
		windows: participants, hosts: hosts, lookups: races, narrator: events,
	}).observe)
}
