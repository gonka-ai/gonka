// Package perf tracks per-host health/ejection, in-flight load, and capability flags.
package perf

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/config"
)

// Tracker's mu guards the host map and the rebuild scratch it reuses. See capacity.md, "Outlier ejection".
type Tracker struct {
	mu            sync.Mutex
	config        *config.Holder
	hosts         map[hostKey]*hostState
	liveEjections []liveEjection
	capability    *capabilityTracker
	inflight      *inflightGauge
	now           func() time.Time
	narrator      hostNarrator
	lastSweep     time.Time
	view          atomic.Pointer[map[hostKey]ejectionView]
}

// ejectionView is what a routing decision asks of one host: the capped verdict and the raw one.
type ejectionView struct {
	ejectedUntil  time.Time
	degradedUntil time.Time
}

// liveEjection carries what the cap sorts on, so the comparison never re-searches the map.
type liveEjection struct {
	key           hostKey
	ejectedUntil  time.Time
	ejectionCount int
}

// Withholding is one host taken out of routing, as the line that explains it names it.
type Withholding struct {
	Participant         string
	Model               string
	Reason              string
	EjectionCount       int
	ConsecutiveFailures int
	FailureRate         float64
	FailureVolume       float64
	WithheldFor         time.Duration
}

// hostNarrator is satisfied by *journal.Journal; each method is called on the edge it names, never per sample. See README.md, "When a host stops taking work".
type hostNarrator interface {
	HostWithheld(withheld Withholding)
	HostReturned(participant, model string, ejectionCount int)
	HostContextLimit(participant, model string, contextLimit, previousContextLimit uint64)
	HostToolsUnsupported(participant, model string)
	HostVersionUnsupported(participant string)
}

func NewTracker(holder *config.Holder, now func() time.Time) *Tracker {
	return &Tracker{
		config:     holder,
		now:        now,
		hosts:      make(map[hostKey]*hostState),
		capability: newCapabilityTracker(),
		inflight:   newInflightGauge(),
	}
}

// SetNarrator binds the journal the tracker's edges are written through; call it before the tracker is shared.
func (t *Tracker) SetNarrator(narrator hostNarrator) {
	t.narrator = narrator
}

func (t *Tracker) FirstContentP75(participant, model string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	host := t.hosts[hostKey{participant: participant, model: model}]
	if host == nil {
		return 0, false
	}
	return host.perf.firstContent.p75(latencyWindowMinimum)
}

// Pressure is how far a host's latency sits above the best it has held, per stage; zero means not yet known.
type Pressure struct {
	FirstContent float64
	Decode       float64
}

// Pressure reads the delay signal the congestion windows narrow on before anything has failed.
func (t *Tracker) Pressure(participant, model string) Pressure {
	t.mu.Lock()
	defer t.mu.Unlock()
	host := t.hosts[hostKey{participant: participant, model: model}]
	if host == nil {
		return Pressure{}
	}
	return Pressure{
		FirstContent: host.perf.firstContent.pressure(),
		Decode:       host.perf.decode.pressure(),
	}
}

func (t *Tracker) TimePerOutputTokenP75(participant, model string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	host := t.hosts[hostKey{participant: participant, model: model}]
	if host == nil {
		return 0, false
	}
	return host.perf.decode.p75(latencyWindowMinimum)
}

// RecordServedMismatch costs a host one unresponsive sample for a stream its own Finish does not sign for, the same currency as a stall.
func (t *Tracker) RecordServedMismatch(participant, model string) {
	t.RecordSample(Sample{ParticipantKey: participant, Model: model, Responsive: false})
}

func (t *Tracker) RecordSample(s Sample) {
	now := t.now()
	perf := t.config.Load().Perf
	key := hostKey{participant: s.ParticipantKey, model: s.Model}

	t.mu.Lock()
	defer t.mu.Unlock()

	host := t.ensureHostLocked(key, perf)
	host.perf.recordSample(s, now)
	ejectedUntilBefore := host.ejection.ejectedUntil
	newEjectionPolicyFromPerf(perf).evaluate(&host.perf, &host.ejection, now)

	staleness := time.Duration(perf.HostStalenessSeconds) * time.Second
	evicted := t.evictStaleLocked(now, staleness)
	if evicted || host.ejection.ejectedUntil != ejectedUntilBefore {
		t.rebuildEjectedViewLocked(now, perf)
	}
	t.narrateEjectionTransition(key, host, now, ejectedUntilBefore)
}

// narrateEjectionTransition runs under t.mu and narrates HostWithheld or HostReturned only on the change, counting a lapse the same sample renews as a new withholding; the state moves with or without a narrator.
func (t *Tracker) narrateEjectionTransition(key hostKey, host *hostState, now, ejectedUntilBefore time.Time) {
	withheldNow := host.ejection.ejected(now)
	switch {
	case withheldNow && (!host.ejection.wasWithheld || host.ejection.ejectedUntil != ejectedUntilBefore):
		host.ejection.wasWithheld = true
		if t.narrator == nil {
			return
		}
		t.narrator.HostWithheld(Withholding{
			Participant:         key.participant,
			Model:               key.model,
			Reason:              host.ejection.trigger.reason,
			EjectionCount:       host.ejection.ejectionCount,
			ConsecutiveFailures: host.ejection.trigger.consecutiveFailures,
			FailureRate:         host.ejection.trigger.failureRate,
			FailureVolume:       host.ejection.trigger.failureVolume,
			WithheldFor:         host.ejection.ejectedUntil.Sub(now),
		})
	case !withheldNow && host.ejection.wasWithheld:
		host.ejection.wasWithheld = false
		if t.narrator != nil {
			t.narrator.HostReturned(key.participant, key.model, host.ejection.ejectionCount)
		}
	}
}

// rebuildEjectedViewLocked republishes both verdicts as one map: every live ejection is degraded, and the per-model cap decides which of them routing actually withholds.
func (t *Tracker) rebuildEjectedViewLocked(now time.Time, perf config.Perf) {
	live := t.liveEjections[:0]
	knownByModel := make(map[string]int, len(t.hosts))
	for key, host := range t.hosts {
		knownByModel[key.model]++
		if host.ejection.ejected(now) {
			live = append(live, liveEjection{key: key, ejectedUntil: host.ejection.ejectedUntil, ejectionCount: host.ejection.ejectionCount})
		}
	}
	t.liveEjections = live

	slices.SortFunc(live, func(first, second liveEjection) int {
		if models := strings.Compare(first.key.model, second.key.model); models != 0 {
			return models
		}
		if rung := cmp.Compare(second.ejectionCount, first.ejectionCount); rung != 0 {
			return rung
		}
		return strings.Compare(first.key.participant, second.key.participant)
	})

	view := make(map[hostKey]ejectionView, len(live))
	allowed, rank := 0, 0
	for index, ejection := range live {
		if index == 0 || ejection.key.model != live[index-1].key.model {
			allowed, rank = maxEjectable(perf, knownByModel[ejection.key.model]), 0
		}
		entry := ejectionView{degradedUntil: ejection.ejectedUntil}
		if rank < allowed {
			entry.ejectedUntil = ejection.ejectedUntil
		}
		view[ejection.key] = entry
		rank++
	}
	t.view.Store(&view)
}

func (t *Tracker) ensureHostLocked(key hostKey, perf config.Perf) *hostState {
	host, ok := t.hosts[key]
	if !ok {
		host = newHostState(time.Duration(perf.EWMAHalfLifeSeconds) * time.Second)
		t.hosts[key] = host
	}
	return host
}

// evictStaleLocked sweeps at most once per tenth of the staleness window. See capacity.md, "Outlier ejection".
func (t *Tracker) evictStaleLocked(now time.Time, staleness time.Duration) bool {
	if now.Sub(t.lastSweep) < staleness/10 {
		return false
	}
	t.lastSweep = now
	evicted := false
	for key, host := range t.hosts {
		if now.Sub(host.perf.lastSeen) > staleness {
			delete(t.hosts, key)
			evicted = true
		}
	}
	return evicted
}

func newEjectionPolicyFromPerf(perf config.Perf) ejectionPolicy {
	return newEjectionPolicy(
		int(perf.ConsecutiveFailThreshold),
		perf.FailureRateThreshold,
		perf.FailureRateMinVolume,
		time.Duration(perf.EjectionBaseSeconds)*time.Second,
		time.Duration(perf.EjectionMaxSeconds)*time.Second,
	)
}

func (t *Tracker) RecordContextLimit(participant, model string, maxTokens uint64) {
	if previous, changed := t.capability.recordContextLimit(participant, model, maxTokens); changed && t.narrator != nil {
		t.narrator.HostContextLimit(participant, model, maxTokens, previous)
	}
}

func (t *Tracker) RecordToolUnsupported(participant, model string) {
	if t.capability.recordToolUnsupported(participant, model) && t.narrator != nil {
		t.narrator.HostToolsUnsupported(participant, model)
	}
}

func (t *Tracker) RecordVersionUnsupported(participant string) {
	if t.capability.recordVersionUnsupported(participant) && t.narrator != nil {
		t.narrator.HostVersionUnsupported(participant)
	}
}

func (t *Tracker) RecordOutputOverrun(participant, model string) {
	t.capability.recordOutputOverrun(participant, model)
}

func (t *Tracker) Acquire(participant string) {
	t.inflight.acquire(participant)
}

func (t *Tracker) Release(participant string) {
	t.inflight.release(participant)
}

// HostState is one tracked participant/model pair as a reader sees it.
type HostState struct {
	Participant        string
	Model              string
	Ejected            bool
	Inflight           int
	TimePerOutputToken time.Duration
}

// Snapshot returns every tracked pair, order unspecified; the decode quantile and in-flight count are read after the host lock releases.
func (t *Tracker) Snapshot() []HostState {
	now := t.now()
	view := t.view.Load()

	type hostDecodeWindow struct {
		key    hostKey
		window latencyWindow
	}
	t.mu.Lock()
	copied := make([]hostDecodeWindow, 0, len(t.hosts))
	for key, host := range t.hosts {
		copied = append(copied, hostDecodeWindow{key: key, window: host.perf.decode})
	}
	t.mu.Unlock()

	states := make([]HostState, 0, len(copied))
	for _, host := range copied {
		decode, _ := host.window.p75(latencyWindowMinimum)
		states = append(states, HostState{
			Participant:        host.key.participant,
			Model:              host.key.model,
			Ejected:            view != nil && now.Before((*view)[host.key].ejectedUntil),
			Inflight:           t.inflight.count(host.key.participant),
			TimePerOutputToken: decode,
		})
	}
	return states
}

// Ejected reads the capped view published at rebuild time. See capacity.md, "Outlier ejection".
func (t *Tracker) Ejected(participant, model string) bool {
	return t.now().Before(t.viewOf(participant, model).ejectedUntil)
}

// Degraded is the verdict before the pool-wide cap. See README.md, "Ejection, and its two views".
func (t *Tracker) Degraded(participant, model string) bool {
	return t.now().Before(t.viewOf(participant, model).degradedUntil)
}

func (t *Tracker) viewOf(participant, model string) ejectionView {
	view := t.view.Load()
	if view == nil {
		return ejectionView{}
	}
	return (*view)[hostKey{participant: participant, model: model}]
}

func maxEjectable(perf config.Perf, knownForModel int) int {
	allowed := int(perf.MaxEjectionFraction * float64(knownForModel))
	if byAvailability := knownForModel - int(perf.MinAvailableHosts); byAvailability < allowed {
		allowed = byAvailability
	}
	if allowed < 0 {
		return 0
	}
	return allowed
}

func (t *Tracker) Capability(participant, model string) (contextLimit, versionRefusals, toolRefusals, contextRefusals uint64) {
	return t.capability.capability(participant, model)
}

func (t *Tracker) OutputOverruns(participant, model string) uint64 {
	return t.capability.overruns(participant, model)
}
