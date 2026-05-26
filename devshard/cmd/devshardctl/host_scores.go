package main

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Tunable parameters. Defaults documented in docs/host-scoring.md#hyperparameters.
// Not atomic — mutate at quiet times (startup or via admin endpoint).
var (
	// Sample ring & gating
	HostScoreWindowSize = 50
	HostScoreMinSamples = 3

	// Layer 1: H2H trigger
	HostScoreH2HMargin = 0.10 // candidate must win ≥0.5+margin to act

	// Layer 2: base timing weights
	HostScoreStreamGamma    = 0.3
	HostScoreNonStreamGamma = 1.0

	// Layer 2: Elo
	HostScoreEloK        = 16.0
	HostScoreEloDefault  = 1500.0
	HostScoreEloAlpha    = 10.0           // ms per Elo-point delta vs default
	HostScoreEloHalfLife = 12 * time.Hour // half-life for stale-rating decay; 0 disables

	// Layer 2: UCB exploration
	HostScoreUCBCoefficient = 300.0 // ms; 0 disables

	// Layer 2: Bradley-Terry exponent (see docs/host-scoring.md#layer-22-elo-credit).
	HostScoreBradleyTerryExp = 2.0

	// Decision margins
	HostScoreSpeedupMargin      = 0.10 // candidate score must be ≤primary·(1-margin)
	HostScoreExplorationEpsilon = 0.05 // 5% forced-exploration when score is ambivalent

	// Per-bucket calibration overrides. See
	// docs/host-scoring.md#per-bucket-calibration for the rationale.
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"lt_1k":    {HalfLife: 3 * time.Hour},  // high traffic → fast adaptation
		"1k_5k":    {HalfLife: 3 * time.Hour},  // high traffic → fast adaptation
		"5k_15k":   {HalfLife: 6 * time.Hour},  // medium traffic
		"15k_30k":  {HalfLife: 6 * time.Hour},  // medium traffic
		"30k_100k": {HalfLife: 9 * time.Hour},  // low traffic → preserve signal across gaps
		"gte_100k": {HalfLife: 12 * time.Hour}, // very low traffic
	}
)

// HostScoreBucketOverride lets a bucket adopt non-default K and half-life.
// K=0 inherits HostScoreEloK. HalfLife<0 inherits HostScoreEloHalfLife;
// HalfLife=0 explicitly disables decay for that bucket.
type HostScoreBucketOverride struct {
	K        float64
	HalfLife time.Duration
}

// hostScoreKForBucket returns the K-factor in effect for a bucket.
func hostScoreKForBucket(bucket string) float64 {
	if o, ok := HostScoreBucketOverrides[bucket]; ok && o.K > 0 {
		return o.K
	}
	return HostScoreEloK
}

// hostScoreHalfLifeForBucket returns the decay half-life in effect for a
// bucket. Negative override means inherit; zero (explicit or inherited)
// disables decay.
func hostScoreHalfLifeForBucket(bucket string) time.Duration {
	if o, ok := HostScoreBucketOverrides[bucket]; ok && o.HalfLife >= 0 {
		return o.HalfLife
	}
	return HostScoreEloHalfLife
}

// hostScoreRandom drives the ε-greedy floor; tests swap it for determinism.
var hostScoreRandom = rand.Float64

type hostScoreKey struct {
	model  string
	host   string // ParticipantKey
	bucket string
}

type hostScoreSample struct {
	Timestamp time.Time `json:"timestamp"`
	TtftMs    float64   `json:"ttft_ms"`
	TotalMs   float64   `json:"total_ms"`
}

type hostScoreRing struct {
	samples []hostScoreSample
	pos     int
}

func (r *hostScoreRing) add(s hostScoreSample) {
	if len(r.samples) < HostScoreWindowSize {
		r.samples = append(r.samples, s)
		r.pos = len(r.samples) % HostScoreWindowSize
		return
	}
	r.samples[r.pos] = s
	r.pos = (r.pos + 1) % HostScoreWindowSize
}

// ordered returns samples in chronological order (oldest first).
func (r *hostScoreRing) ordered() []hostScoreSample {
	if len(r.samples) < HostScoreWindowSize {
		out := make([]hostScoreSample, len(r.samples))
		copy(out, r.samples)
		return out
	}
	out := make([]hostScoreSample, HostScoreWindowSize)
	for i := 0; i < HostScoreWindowSize; i++ {
		out[i] = r.samples[(r.pos+i)%HostScoreWindowSize]
	}
	return out
}

// percentile returns the q-th percentile of (ttftMs, totalMs) over current
// samples. q in [0,1]. Uses linear interpolation between adjacent ranks.
func (r *hostScoreRing) percentile(q float64) (ttft, total float64) {
	n := len(r.samples)
	if n == 0 {
		return 0, 0
	}
	ttfts := make([]float64, n)
	totals := make([]float64, n)
	for i, s := range r.samples {
		ttfts[i] = s.TtftMs
		totals[i] = s.TotalMs
	}
	sort.Float64s(ttfts)
	sort.Float64s(totals)
	return interpPercentile(ttfts, q), interpPercentile(totals, q)
}

func interpPercentile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 || q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// HostScoreSnapshot is the per-key public view emitted by the admin endpoint.
type HostScoreSnapshot struct {
	Model          string  `json:"model"`
	Host           string  `json:"host"`
	Bucket         string  `json:"bucket"`
	Samples        int     `json:"samples"`
	BucketTotalN   int     `json:"bucket_total_samples"` // N for UCB denominator
	TtftP50Ms      float64 `json:"ttft_p50_ms"`
	TtftP90Ms      float64 `json:"ttft_p90_ms"`
	TotalP50Ms     float64 `json:"total_p50_ms"`
	TotalP90Ms     float64 `json:"total_p90_ms"`
	Elo            float64 `json:"elo"`
	UCBBonus       float64 `json:"ucb_bonus_ms"` // exploration credit subtracted from score
	ScoreStream    float64 `json:"score_stream"`
	ScoreNonStream float64 `json:"score_non_stream"`
}

// HostScoreState is the per-key persistent shape for SQLite. UpdatedAt seeds
// Elo time-decay across restarts (docs/host-scoring.md#regime-change).
type HostScoreState struct {
	Model     string            `json:"model"`
	Host      string            `json:"host"`
	Bucket    string            `json:"bucket"`
	Elo       float64           `json:"elo"`
	Samples   []hostScoreSample `json:"samples"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// HostScoreTracker holds per-(model,host,bucket) sliding sample rings + Elo.
// Formula and rationale: docs/host-scoring.md.
type HostScoreTracker struct {
	mu            sync.RWMutex
	hosts         map[hostScoreKey]*hostScoreRing
	elo           map[hostScoreKey]float64
	eloUpdatedAt  map[hostScoreKey]time.Time
	pairwise      *PairwiseTracker // optional H2H source; may be nil in tests
	now           func() time.Time // injectable clock for tests
}

func NewHostScoreTracker(pairwise *PairwiseTracker) *HostScoreTracker {
	return &HostScoreTracker{
		hosts:        make(map[hostScoreKey]*hostScoreRing),
		elo:          make(map[hostScoreKey]float64),
		eloUpdatedAt: make(map[hostScoreKey]time.Time),
		pairwise:     pairwise,
		now:          time.Now,
	}
}

// decayedEloLocked pulls Elo toward default by the bucket's half-life
// exponential decay since last update; 0 disables.
func (t *HostScoreTracker) decayedEloLocked(key hostScoreKey, now time.Time) float64 {
	raw := t.elo[key]
	if raw == 0 { // map zero-value = never K-updated, treat as default
		return HostScoreEloDefault
	}
	halfLife := hostScoreHalfLifeForBucket(key.bucket)
	if halfLife <= 0 {
		return raw
	}
	lastUpdate, ok := t.eloUpdatedAt[key]
	if !ok || lastUpdate.IsZero() {
		return raw
	}
	age := now.Sub(lastUpdate)
	if age <= 0 {
		return raw
	}
	multiplier := math.Exp(-math.Ln2 * float64(age) / float64(halfLife))
	return HostScoreEloDefault + (raw-HostScoreEloDefault)*multiplier
}

// RecordRequest appends a sample per scoreable host and runs two K/2 Elo
// updates per pair (TTFT + Total). See docs/host-scoring.md#layer-22-elo-credit.
func (t *HostScoreTracker) RecordRequest(rec RequestRecord) {
	if t == nil || len(rec.Hosts) == 0 {
		return
	}
	bucket := requestShapeBucket(rec.InputTokens)
	now := rec.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	eligible := make([]HostInvolvement, 0, len(rec.Hosts))
	for _, h := range rec.Hosts {
		if h.ExcludePairwise || h.ParticipantKey == "" {
			continue
		}
		eligible = append(eligible, h)
	}
	if len(eligible) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, h := range eligible {
		if !scoreablePairwiseHost(h) {
			continue
		}
		key := hostScoreKey{model: rec.Model, host: h.ParticipantKey, bucket: bucket}
		ring := t.hosts[key]
		if ring == nil {
			ring = &hostScoreRing{samples: make([]hostScoreSample, 0, HostScoreWindowSize)}
			t.hosts[key] = ring
		}
		ring.add(hostScoreSample{Timestamp: now, TtftMs: h.FirstTokenMs, TotalMs: h.TotalTimeMs})
	}

	halfK := hostScoreKForBucket(bucket) / 2
	for i := 0; i < len(eligible); i++ {
		for j := i + 1; j < len(eligible); j++ {
			a, b := eligible[i], eligible[j]
			if a.ParticipantKey == b.ParticipantKey {
				continue
			}
			keyA := hostScoreKey{model: rec.Model, host: a.ParticipantKey, bucket: bucket}
			keyB := hostScoreKey{model: rec.Model, host: b.ParticipantKey, bucket: bucket}
			SaTTFT, SbTTFT := pairOutcome(a, b, true)
			t.updateEloLocked(keyA, keyB, SaTTFT, SbTTFT, halfK, now)
			SaTotal, SbTotal := pairOutcome(a, b, false)
			t.updateEloLocked(keyA, keyB, SaTotal, SbTotal, halfK, now)
		}
	}
}

// pairOutcome returns (Sa, Sb) for one metric dimension; outcome matrix
// in docs/host-scoring.md#layer-22-elo-credit.
func pairOutcome(a, b HostInvolvement, useTTFT bool) (Sa, Sb float64) {
	aOK := metricAvailable(a, useTTFT)
	bOK := metricAvailable(b, useTTFT)
	switch {
	case aOK && bOK:
		Sa = bradleyTerryScore(metricFor(a, useTTFT), metricFor(b, useTTFT))
		return Sa, 1.0 - Sa
	case aOK && !bOK:
		return 1.0, 0.0
	case !aOK && bOK:
		return 0.0, 1.0
	default:
		return 0.0, 0.0
	}
}

// bradleyTerryScore: Sa = mb^k / (ma^k + mb^k) for LOWER-is-better metrics.
// See docs/host-scoring.md#layer-22-elo-credit.
func bradleyTerryScore(ma, mb float64) float64 {
	if ma <= 0 || mb <= 0 {
		return 0.5
	}
	ra := math.Pow(ma, HostScoreBradleyTerryExp)
	rb := math.Pow(mb, HostScoreBradleyTerryExp)
	return rb / (ra + rb)
}

func metricAvailable(h HostInvolvement, useTTFT bool) bool {
	if !h.Responsive {
		return false
	}
	if useTTFT {
		return h.FirstTokenMs > 0
	}
	return h.Finished && h.TotalTimeMs > 0
}

func metricFor(h HostInvolvement, useTTFT bool) float64 {
	if useTTFT {
		return h.FirstTokenMs
	}
	return h.TotalTimeMs
}

// updateEloLocked applies a K-factor Elo step after decaying both ratings.
// See docs/host-scoring.md#layer-22-elo-credit.
func (t *HostScoreTracker) updateEloLocked(keyA, keyB hostScoreKey, Sa, Sb, K float64, now time.Time) {
	Ra := t.decayedEloLocked(keyA, now)
	Rb := t.decayedEloLocked(keyB, now)
	Ea := 1.0 / (1 + math.Pow(10, (Rb-Ra)/400))
	Eb := 1.0 - Ea
	t.elo[keyA] = Ra + K*(Sa-Ea)
	t.elo[keyB] = Rb + K*(Sb-Eb)
	t.eloUpdatedAt[keyA] = now
	t.eloUpdatedAt[keyB] = now
}

// ScoreHost returns a lower-is-better routing score (0,false if no data).
// Two layers (H2H rate then Elo+timing+UCB); see docs/host-scoring.md#the-score-formula.
func (t *HostScoreTracker) ScoreHost(model, host, bucket string, stream bool, opponent string) (float64, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := hostScoreKey{model: model, host: host, bucket: bucket}
	ring := t.hosts[key]
	if ring == nil || len(ring.samples) < HostScoreMinSamples {
		return 0, false
	}

	if opponent != "" && t.pairwise != nil {
		if rate, n, ok := t.pairwise.H2HWinRate(model, bucket, host, opponent); ok && n >= HostScoreMinSamples {
			return 1.0 - rate, true
		}
	}

	gamma := HostScoreNonStreamGamma
	if stream {
		gamma = HostScoreStreamGamma
	}
	ttft, total := ring.percentile(0.50)
	base := (1-gamma)*ttft + gamma*total
	elo := t.decayedEloLocked(key, t.now())
	ucb := t.ucbBonusLocked(model, bucket, len(ring.samples))
	return base - HostScoreEloAlpha*(elo-HostScoreEloDefault) - ucb, true
}

// ucbBonusLocked computes c·√(ln N_bucket / n_host); caller holds mu.
func (t *HostScoreTracker) ucbBonusLocked(model, bucket string, n int) float64 {
	if HostScoreUCBCoefficient <= 0 || n <= 0 {
		return 0
	}
	total := 0
	for k, ring := range t.hosts {
		if k.model == model && k.bucket == bucket {
			total += len(ring.samples)
		}
	}
	if total <= n {
		return 0
	}
	return HostScoreUCBCoefficient * math.Sqrt(math.Log(float64(total))/float64(n))
}

// Snapshot returns the public view of all tracked keys, sorted for stable
// admin output (model, bucket, host).
func (t *HostScoreTracker) Snapshot() []HostScoreSnapshot {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	type bucketCacheKey struct{ model, bucket string }
	bucketTotals := make(map[bucketCacheKey]int)
	totalFor := func(model, bucket string) int {
		k := bucketCacheKey{model: model, bucket: bucket}
		if v, ok := bucketTotals[k]; ok {
			return v
		}
		total := 0
		for k2, ring := range t.hosts {
			if k2.model == model && k2.bucket == bucket {
				total += len(ring.samples)
			}
		}
		bucketTotals[k] = total
		return total
	}
	now := t.now()
	out := make([]HostScoreSnapshot, 0, len(t.hosts))
	for key, ring := range t.hosts {
		ttftP50, totalP50 := ring.percentile(0.50)
		ttftP90, totalP90 := ring.percentile(0.90)
		elo := t.decayedEloLocked(key, now)
		eloBonus := HostScoreEloAlpha * (elo - HostScoreEloDefault)
		bucketN := totalFor(key.model, key.bucket)
		ucb := t.ucbBonusLocked(key.model, key.bucket, len(ring.samples))
		out = append(out, HostScoreSnapshot{
			Model:          key.model,
			Host:           key.host,
			Bucket:         key.bucket,
			Samples:        len(ring.samples),
			BucketTotalN:   bucketN,
			TtftP50Ms:      ttftP50,
			TtftP90Ms:      ttftP90,
			TotalP50Ms:     totalP50,
			TotalP90Ms:     totalP90,
			Elo:            elo,
			UCBBonus:       ucb,
			ScoreStream:    (1-HostScoreStreamGamma)*ttftP50 + HostScoreStreamGamma*totalP50 - eloBonus - ucb,
			ScoreNonStream: (1-HostScoreNonStreamGamma)*ttftP50 + HostScoreNonStreamGamma*totalP50 - eloBonus - ucb,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		if out[i].Bucket != out[j].Bucket {
			return out[i].Bucket < out[j].Bucket
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// PersistState returns per-key state for SQLite upsert; UpdatedAt is the
// per-row last-K-update time, not the snapshot time.
func (t *HostScoreTracker) PersistState() []HostScoreState {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	fallback := t.now().UTC() // for keys with samples but no K-update yet (Elo also default)
	out := make([]HostScoreState, 0, len(t.hosts))
	for key, ring := range t.hosts {
		elo := t.elo[key]
		if elo == 0 {
			elo = HostScoreEloDefault
		}
		samples := make([]hostScoreSample, len(ring.samples))
		copy(samples, ring.samples)
		updatedAt := t.eloUpdatedAt[key]
		if updatedAt.IsZero() {
			updatedAt = fallback
		}
		out = append(out, HostScoreState{
			Model:     key.model,
			Host:      key.host,
			Bucket:    key.bucket,
			Elo:       elo,
			Samples:   samples,
			UpdatedAt: updatedAt.UTC(),
		})
	}
	return out
}

// Restore replaces in-memory state from persisted rows; per-row UpdatedAt
// seeds eloUpdatedAt so decay continues across restart.
func (t *HostScoreTracker) Restore(states []HostScoreState) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hosts = make(map[hostScoreKey]*hostScoreRing, len(states))
	t.elo = make(map[hostScoreKey]float64, len(states))
	t.eloUpdatedAt = make(map[hostScoreKey]time.Time, len(states))
	for _, s := range states {
		key := hostScoreKey{model: s.Model, host: s.Host, bucket: s.Bucket}
		samples := s.Samples
		if len(samples) > HostScoreWindowSize {
			samples = samples[len(samples)-HostScoreWindowSize:]
		}
		ring := &hostScoreRing{samples: make([]hostScoreSample, len(samples), HostScoreWindowSize)}
		copy(ring.samples, samples)
		ring.pos = len(ring.samples) % HostScoreWindowSize
		t.hosts[key] = ring
		if s.Elo != 0 {
			t.elo[key] = s.Elo
		}
		if !s.UpdatedAt.IsZero() {
			t.eloUpdatedAt[key] = s.UpdatedAt
		}
	}
}
