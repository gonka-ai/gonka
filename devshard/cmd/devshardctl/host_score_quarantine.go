package main

import (
	"sort"
	"sync"
	"time"
)

// Layer 3 host_score performance quarantine. Algorithm + tradeoffs in
// docs/host-scoring.md#performance-quarantine.
// Compile-time array dimensions. To change, refactor to slice-based rings.
const (
	hostScoreSampleRingCapacity = 20
	hostScoreSlidingWindow      = 5
)

// Runtime tunables (matches the var pattern in host_scores.go). Not atomic —
// mutate at quiet times via admin endpoint.
var (
	HostScoreQuarantineMinSamplesPerHost  = 3
	HostScoreQuarantineMinQualifyingHosts = 3
	HostScoreQuarantineBadInWindow        = 3
	HostScoreQuarantineBaseDuration       = 30 * time.Minute
	HostScoreQuarantineMaxDuration        = 120 * time.Minute
	HostScoreQuarantineHistoryWindow      = 4 * time.Hour
)

type hostScoreQuarantine struct {
	mu       sync.Mutex
	samples  map[string]map[string]*hostTTFTRing    // bucket → host → ring
	states   map[string]map[string]*bucketQuarState // bucket → host → strike/quarantine
	now      func() time.Time
	onEnter  func(host, bucket string, until time.Time)
	onExpire func(host, bucket string)
}

func newHostScoreQuarantine() *hostScoreQuarantine {
	return &hostScoreQuarantine{
		samples: make(map[string]map[string]*hostTTFTRing),
		states:  make(map[string]map[string]*bucketQuarState),
		now:     time.Now,
	}
}

type hostTTFTRing struct {
	buf [hostScoreSampleRingCapacity]float64
	n   int
	pos int
}

func (r *hostTTFTRing) add(v float64) {
	r.buf[r.pos] = v
	r.pos = (r.pos + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

func (r *hostTTFTRing) values() []float64 {
	out := make([]float64, r.n)
	copy(out, r.buf[:r.n])
	return out
}

type bucketQuarState struct {
	window          [hostScoreSlidingWindow]bool // true = bad
	windowN         int
	windowPos       int
	quarantineUntil time.Time   // zero = not quarantined
	history         []time.Time // entries within HostScoreQuarantineHistoryWindow
}

func (s *bucketQuarState) recordOutcome(bad bool) {
	s.window[s.windowPos] = bad
	s.windowPos = (s.windowPos + 1) % len(s.window)
	if s.windowN < len(s.window) {
		s.windowN++
	}
}

func (s *bucketQuarState) badCount() int {
	n := 0
	for i := 0; i < s.windowN; i++ {
		if s.window[i] {
			n++
		}
	}
	return n
}

func (s *bucketQuarState) clearWindow() {
	s.window = [hostScoreSlidingWindow]bool{}
	s.windowN = 0
	s.windowPos = 0
}

// nextQuarantineDuration: 30m, 60m, 120m (cap), reset after 4h idle.
func (s *bucketQuarState) nextQuarantineDuration(now time.Time) time.Duration {
	cutoff := now.Add(-HostScoreQuarantineHistoryWindow)
	kept := s.history[:0]
	for _, t := range s.history {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.history = kept
	dur := HostScoreQuarantineBaseDuration << len(s.history)
	if dur > HostScoreQuarantineMaxDuration {
		dur = HostScoreQuarantineMaxDuration
	}
	s.history = append(s.history, now)
	return dur
}

// recordSample is the main entry point. No-op unless policy is host_score.
func (q *hostScoreQuarantine) recordSample(host, bucket string, ttftMs float64, responsive bool) {
	if RedundancySpeedPolicy != RedundancySpeedPolicyHostScore {
		return
	}
	if host == "" || bucket == "" || !responsive || ttftMs <= 0 {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	bucketSamples := q.samples[bucket]
	if bucketSamples == nil {
		bucketSamples = make(map[string]*hostTTFTRing)
		q.samples[bucket] = bucketSamples
	}
	ring := bucketSamples[host]
	if ring == nil {
		ring = &hostTTFTRing{}
		bucketSamples[host] = ring
	}
	ring.add(ttftMs)

	threshold, ok := q.bucketThresholdLocked(bucketSamples, host)
	if !ok {
		return
	}

	bucketStates := q.states[bucket]
	if bucketStates == nil {
		bucketStates = make(map[string]*bucketQuarState)
		q.states[bucket] = bucketStates
	}
	st := bucketStates[host]
	if st == nil {
		st = &bucketQuarState{}
		bucketStates[host] = st
	}

	now := q.now()
	if !st.quarantineUntil.IsZero() && now.After(st.quarantineUntil) {
		st.quarantineUntil = time.Time{}
		st.clearWindow()
		if q.onExpire != nil {
			q.onExpire(host, bucket)
		}
	}
	if !st.quarantineUntil.IsZero() {
		return
	}

	st.recordOutcome(ttftMs > threshold)
	if st.windowN >= HostScoreQuarantineBadInWindow && st.badCount() >= HostScoreQuarantineBadInWindow {
		dur := st.nextQuarantineDuration(now)
		st.quarantineUntil = now.Add(dur)
		st.clearWindow()
		if q.onEnter != nil {
			q.onEnter(host, bucket, st.quarantineUntil)
		}
	}
}

// bucketThresholdLocked: p90 over qualifying hosts (≥ MinSamplesPerHost each,
// ≥ MinQualifyingHosts total), excluding excludeHost to prevent self-pollution.
func (q *hostScoreQuarantine) bucketThresholdLocked(bucketSamples map[string]*hostTTFTRing, excludeHost string) (float64, bool) {
	pool := make([]float64, 0, len(bucketSamples)*hostScoreSampleRingCapacity)
	qualifying := 0
	for host, ring := range bucketSamples {
		if host == excludeHost {
			continue
		}
		if ring.n < HostScoreQuarantineMinSamplesPerHost {
			continue
		}
		qualifying++
		pool = append(pool, ring.values()...)
	}
	if qualifying < HostScoreQuarantineMinQualifyingHosts {
		return 0, false
	}
	sort.Float64s(pool)
	if len(pool) == 0 {
		return 0, false
	}
	idx := int(0.9 * float64(len(pool)-1))
	if idx < 0 {
		idx = 0
	}
	return pool[idx], true
}

// isQuarantined returns true if any bucket has an active lockout for host.
func (q *hostScoreQuarantine) isQuarantined(host string) bool {
	if RedundancySpeedPolicy != RedundancySpeedPolicyHostScore {
		return false
	}
	if host == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, byHost := range q.states {
		if st, ok := byHost[host]; ok {
			if !st.quarantineUntil.IsZero() && now.Before(st.quarantineUntil) {
				return true
			}
		}
	}
	return false
}

// clearAll releases every (bucket) quarantine for host and clears strike
// windows. History is preserved so the backoff still penalises repeats.
func (q *hostScoreQuarantine) clearAll(host string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleared := false
	for _, byHost := range q.states {
		if st, ok := byHost[host]; ok {
			if !st.quarantineUntil.IsZero() {
				cleared = true
			}
			st.quarantineUntil = time.Time{}
			st.clearWindow()
		}
	}
	return cleared
}
