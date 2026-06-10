package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	RedundancySpeedPolicyLegacy   = "legacy"
	RedundancySpeedPolicyHybrid   = "hybrid"
	RedundancySpeedPolicyPairwise = "pairwise"
)

var (
	RedundancySpeedPolicy           = RedundancySpeedPolicyHybrid
	PairwiseBudgetPercentile        = 0.90
	PairwiseMaxProactiveAttempts    = 3
	PairwiseMinDirectComparisons    = 4
	PairwiseChainedConfidenceDecay  = 0.75
	PairwiseABSampleRate            = 0.05
	PairwiseABSparseSampleRate      = 0.20
	PairwiseABSparseSampleThreshold = 3
	PairwiseWinnerHold              = 500 * time.Millisecond
	PairwiseWinnerHoldMinSpeedup    = 0.10
	PairwiseWinnerHoldMinSamples    = 6
)

type pairwiseKey struct {
	model  string
	a      string
	b      string
	bucket string
}

type PairwiseComparison struct {
	Timestamp     time.Time `json:"timestamp"`
	Model         string    `json:"model"`
	ParticipantA  string    `json:"participant_a"`
	ParticipantB  string    `json:"participant_b"`
	ShapeBucket   string    `json:"request_shape_bucket"`
	InputTokens   uint64    `json:"input_tokens"`
	ATotalMs      float64   `json:"a_total_ms"`
	BTotalMs      float64   `json:"b_total_ms"`
	RatioAToB     float64   `json:"ratio_a_to_b"`
	AFirstTokenMs float64   `json:"a_first_token_ms"`
	BFirstTokenMs float64   `json:"b_first_token_ms"`
	AFinished     bool      `json:"a_finished"`
	BFinished     bool      `json:"b_finished"`
	AResponsive   bool      `json:"a_responsive"`
	BResponsive   bool      `json:"b_responsive"`
	AWinner       bool      `json:"a_winner"`
	BWinner       bool      `json:"b_winner"`
}

type PairwiseSummary struct {
	Model             string               `json:"model"`
	ParticipantA      string               `json:"participant_a"`
	ParticipantB      string               `json:"participant_b"`
	ShapeBucket       string               `json:"request_shape_bucket"`
	SampleCount       int                  `json:"sample_count"`
	AvgATotalMs       float64              `json:"avg_a_total_ms"`
	AvgBTotalMs       float64              `json:"avg_b_total_ms"`
	AvgRatioAToB      float64              `json:"avg_ratio_a_to_b"`
	AvgSpeedupAToB    float64              `json:"avg_speedup_a_to_b"`
	Confidence        float64              `json:"confidence"`
	LastUpdated       string               `json:"last_updated"`
	RecentComparisons []PairwiseComparison `json:"recent_comparisons,omitempty"`
}

type pairwiseRing struct {
	comparisons []PairwiseComparison
	pos         int
}

type PairwiseTracker struct {
	mu              sync.RWMutex
	pairs           map[pairwiseKey]*pairwiseRing
	failedFollowUps map[pairwiseKey]time.Time
}

func NewPairwiseTracker() *PairwiseTracker {
	return &PairwiseTracker{
		pairs:           make(map[pairwiseKey]*pairwiseRing),
		failedFollowUps: make(map[pairwiseKey]time.Time),
	}
}

func requestShapeBucket(inputTokens uint64) string {
	switch {
	case inputTokens < 1_000:
		return "lt_1k"
	case inputTokens < 5_000:
		return "1k_5k"
	case inputTokens < 15_000:
		return "5k_15k"
	case inputTokens < 30_000:
		return "15k_30k"
	case inputTokens < 100_000:
		return "30k_100k"
	default:
		return "gte_100k"
	}
}

func (t *PairwiseTracker) RecordRequest(rec RequestRecord) {
	if t == nil || len(rec.Hosts) < 2 {
		return
	}
	bucket := requestShapeBucket(rec.InputTokens)
	now := rec.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	for i := range rec.Hosts {
		a := rec.Hosts[i]
		if !scoreablePairwiseHost(a) {
			continue
		}
		for j := range rec.Hosts {
			if i == j {
				continue
			}
			b := rec.Hosts[j]
			if !scoreablePairwiseHost(b) {
				continue
			}
			t.add(PairwiseComparison{
				Timestamp:     now,
				Model:         rec.Model,
				ParticipantA:  a.ParticipantKey,
				ParticipantB:  b.ParticipantKey,
				ShapeBucket:   bucket,
				InputTokens:   rec.InputTokens,
				ATotalMs:      a.TotalTimeMs,
				BTotalMs:      b.TotalTimeMs,
				RatioAToB:     a.TotalTimeMs / b.TotalTimeMs,
				AFirstTokenMs: a.FirstTokenMs,
				BFirstTokenMs: b.FirstTokenMs,
				AFinished:     a.Finished,
				BFinished:     b.Finished,
				AResponsive:   a.Responsive,
				BResponsive:   b.Responsive,
				AWinner:       a.Winner,
				BWinner:       b.Winner,
			})
		}
	}
	t.recordFailedFollowUps(rec, bucket, now)
}

func scoreablePairwiseHost(h HostInvolvement) bool {
	return !h.ExcludePairwise && h.ParticipantKey != "" && h.Finished && h.Responsive && h.TotalTimeMs > 0
}

func failedPairwiseHost(h HostInvolvement) bool {
	return !h.ExcludePairwise && h.ParticipantKey != "" && !scoreablePairwiseHost(h)
}

func (t *PairwiseTracker) recordFailedFollowUps(rec RequestRecord, bucket string, now time.Time) {
	var successes []HostInvolvement
	var failures []HostInvolvement
	for _, h := range rec.Hosts {
		switch {
		case scoreablePairwiseHost(h):
			successes = append(successes, h)
		case failedPairwiseHost(h):
			failures = append(failures, h)
		}
	}
	if len(successes) == 0 || len(failures) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failedFollowUps == nil {
		t.failedFollowUps = make(map[pairwiseKey]time.Time)
	}
	for _, a := range successes {
		for _, b := range failures {
			if a.ParticipantKey == b.ParticipantKey {
				continue
			}
			key := pairwiseKey{model: rec.Model, a: a.ParticipantKey, b: b.ParticipantKey, bucket: bucket}
			t.failedFollowUps[key] = now
		}
	}
}

func (t *PairwiseTracker) add(c PairwiseComparison) {
	if c.ParticipantA == "" || c.ParticipantB == "" || c.ParticipantA == c.ParticipantB || c.BTotalMs <= 0 {
		return
	}
	key := pairwiseKey{model: c.Model, a: c.ParticipantA, b: c.ParticipantB, bucket: c.ShapeBucket}
	t.mu.Lock()
	defer t.mu.Unlock()
	ring := t.pairs[key]
	if ring == nil {
		ring = &pairwiseRing{comparisons: make([]PairwiseComparison, 0, 10)}
		t.pairs[key] = ring
	}
	if len(ring.comparisons) < 10 {
		ring.comparisons = append(ring.comparisons, c)
		ring.pos = len(ring.comparisons) % 10
		return
	}
	ring.comparisons[ring.pos] = c
	ring.pos = (ring.pos + 1) % 10
}

func (t *PairwiseTracker) Summaries() []PairwiseSummary {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]PairwiseSummary, 0, len(t.pairs))
	for key, ring := range t.pairs {
		out = append(out, summarizePairwise(key, ring.ordered()))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		if out[i].ShapeBucket != out[j].ShapeBucket {
			return out[i].ShapeBucket < out[j].ShapeBucket
		}
		if out[i].ParticipantA != out[j].ParticipantA {
			return out[i].ParticipantA < out[j].ParticipantA
		}
		return out[i].ParticipantB < out[j].ParticipantB
	})
	return out
}

func (r *pairwiseRing) ordered() []PairwiseComparison {
	if r == nil || len(r.comparisons) == 0 {
		return nil
	}
	if len(r.comparisons) < 10 {
		out := make([]PairwiseComparison, len(r.comparisons))
		copy(out, r.comparisons)
		return out
	}
	out := make([]PairwiseComparison, 0, len(r.comparisons))
	for i := 0; i < len(r.comparisons); i++ {
		idx := (r.pos + i) % len(r.comparisons)
		out = append(out, r.comparisons[idx])
	}
	return out
}

func summarizePairwise(key pairwiseKey, comparisons []PairwiseComparison) PairwiseSummary {
	s := PairwiseSummary{
		Model:             key.model,
		ParticipantA:      key.a,
		ParticipantB:      key.b,
		ShapeBucket:       key.bucket,
		SampleCount:       len(comparisons),
		RecentComparisons: comparisons,
	}
	if len(comparisons) == 0 {
		return s
	}
	var aTotal, bTotal, ratioTotal float64
	for _, c := range comparisons {
		aTotal += c.ATotalMs
		bTotal += c.BTotalMs
		ratioTotal += c.RatioAToB
		if c.Timestamp.After(parsePairwiseSummaryTime(s.LastUpdated)) {
			s.LastUpdated = c.Timestamp.Format(time.RFC3339Nano)
		}
	}
	n := float64(len(comparisons))
	s.AvgATotalMs = aTotal / n
	s.AvgBTotalMs = bTotal / n
	s.AvgRatioAToB = ratioTotal / n
	if s.AvgRatioAToB > 1 {
		s.AvgSpeedupAToB = 1 - 1/s.AvgRatioAToB
	}
	s.Confidence = pairwiseSampleConfidence(len(comparisons))
	return s
}

func parsePairwiseSummaryTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func pairwiseSampleConfidence(samples int) float64 {
	switch {
	case samples <= 0:
		return 0
	case samples == 1:
		return 0.33
	case samples == 2:
		return 0.66
	default:
		return 1
	}
}

func (t *PairwiseTracker) EstimateRatio(model string, inputTokens uint64, a, b string, intermediates []string) (float64, float64, bool) {
	if t == nil || a == "" || b == "" || a == b {
		return 0, 0, false
	}
	bucket := requestShapeBucket(inputTokens)
	if ratio, confidence, samples, ok := t.directRatio(model, bucket, a, b); ok && samples >= PairwiseMinDirectComparisons {
		return ratio, confidence, true
	}
	if len(intermediates) == 0 {
		if ratio, confidence, samples, ok := t.directRatio(model, bucket, a, b); ok {
			return ratio, confidence * pairwiseSampleConfidence(samples), true
		}
		return 0, 0, false
	}
	path := append([]string{a}, intermediates...)
	path = append(path, b)
	ratio := 1.0
	confidence := 1.0
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := 0; i < len(path)-1; i++ {
		key := pairwiseKey{model: model, a: path[i], b: path[i+1], bucket: bucket}
		ring := t.pairs[key]
		if ring == nil || len(ring.comparisons) == 0 {
			return 0, 0, false
		}
		s := summarizePairwise(key, ring.ordered())
		if s.AvgRatioAToB <= 0 {
			return 0, 0, false
		}
		ratio *= s.AvgRatioAToB
		confidence = math.Min(confidence, s.Confidence)
	}
	confidence *= math.Pow(PairwiseChainedConfidenceDecay, float64(len(path)-2))
	return ratio, confidence, true
}

func (t *PairwiseTracker) directRatio(model, bucket, a, b string) (float64, float64, int, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := pairwiseKey{model: model, a: a, b: b, bucket: bucket}
	ring := t.pairs[key]
	if ring == nil || len(ring.comparisons) == 0 {
		return 0, 0, 0, false
	}
	s := summarizePairwise(key, ring.ordered())
	return s.AvgRatioAToB, s.Confidence, s.SampleCount, s.AvgRatioAToB > 0
}

func (t *PairwiseTracker) DirectSampleCount(model string, inputTokens uint64, a, b string) int {
	if t == nil || a == "" || b == "" || a == b {
		return 0
	}
	bucket := requestShapeBucket(inputTokens)
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := pairwiseKey{model: model, a: a, b: b, bucket: bucket}
	ring := t.pairs[key]
	if ring == nil {
		return 0
	}
	return len(ring.comparisons)
}

func (t *PairwiseTracker) NeedsFailedComparisonFollowUp(model string, inputTokens uint64, a, failedB, c string) bool {
	if t == nil || a == "" || failedB == "" || c == "" || a == failedB || a == c || failedB == c {
		return false
	}
	bucket := requestShapeBucket(inputTokens)
	t.mu.Lock()
	defer t.mu.Unlock()
	failedKey := pairwiseKey{model: model, a: a, b: failedB, bucket: bucket}
	if _, ok := t.failedFollowUps[failedKey]; !ok {
		return false
	}
	acKey := pairwiseKey{model: model, a: a, b: c, bucket: bucket}
	if ring := t.pairs[acKey]; ring != nil && len(ring.comparisons) >= PairwiseMinDirectComparisons {
		delete(t.failedFollowUps, failedKey)
		return false
	}
	return true
}

func (t *PairwiseTracker) HoldEligible(model string, inputTokens uint64, a, b string) (float64, int, bool) {
	if t == nil || a == "" || b == "" || a == b {
		return 0, 0, false
	}
	bucket := requestShapeBucket(inputTokens)
	ratio, _, samples, ok := t.directRatio(model, bucket, a, b)
	if !ok || samples < PairwiseWinnerHoldMinSamples || ratio <= 1 {
		return 0, samples, false
	}
	speedup := 1 - 1/ratio
	return speedup, samples, speedup >= PairwiseWinnerHoldMinSpeedup
}

func (t *PairwiseTracker) SpeedupCutoff(model string, inputTokens uint64) (float64, bool) {
	return t.SpeedupCutoffForParticipants(model, inputTokens, nil)
}

func (t *PairwiseTracker) SpeedupCutoffForParticipants(model string, inputTokens uint64, participantAvailable func(string) bool) (float64, bool) {
	if t == nil {
		return 0, false
	}
	bucket := requestShapeBucket(inputTokens)
	t.mu.RLock()
	defer t.mu.RUnlock()
	var speedups []float64
	for key, ring := range t.pairs {
		if key.model != model || key.bucket != bucket || ring == nil || len(ring.comparisons) < PairwiseMinDirectComparisons {
			continue
		}
		if participantAvailable != nil && (!participantAvailable(key.a) || !participantAvailable(key.b)) {
			continue
		}
		s := summarizePairwise(key, ring.ordered())
		if s.AvgSpeedupAToB > 0 {
			speedups = append(speedups, s.AvgSpeedupAToB*s.Confidence)
		}
	}
	if len(speedups) == 0 {
		return 0, false
	}
	sort.Float64s(speedups)
	p := PairwiseBudgetPercentile
	if p <= 0 || p >= 1 {
		p = 0.90
	}
	idx := int(math.Ceil(p*float64(len(speedups)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(speedups) {
		idx = len(speedups) - 1
	}
	return speedups[idx], true
}
