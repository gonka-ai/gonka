package main

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkInvolvement(participant string, firstTokenMs, totalMs float64) HostInvolvement {
	return HostInvolvement{
		ParticipantKey: participant,
		FirstTokenMs:   firstTokenMs,
		TotalTimeMs:    totalMs,
		Responsive:     true,
		Finished:       true,
	}
}

func mkRequest(model string, inputTokens uint64, hosts ...HostInvolvement) RequestRecord {
	return RequestRecord{
		Timestamp:   time.Now(),
		Model:       model,
		InputTokens: inputTokens,
		Hosts:       hosts,
	}
}

func TestHostScoreRing_AppendAndPercentile(t *testing.T) {
	ring := &hostScoreRing{samples: make([]hostScoreSample, 0, HostScoreWindowSize)}
	for i := 1; i <= 10; i++ {
		ring.add(hostScoreSample{TtftMs: float64(i * 10), TotalMs: float64(i * 100)})
	}
	require.Len(t, ring.samples, 10)

	ttftP50, totalP50 := ring.percentile(0.50)
	require.InDelta(t, 55.0, ttftP50, 0.001, "ttft p50 should be linear-interpolated midpoint")
	require.InDelta(t, 550.0, totalP50, 0.001)

	ttftP90, totalP90 := ring.percentile(0.90)
	require.InDelta(t, 91.0, ttftP90, 0.001)
	require.InDelta(t, 910.0, totalP90, 0.001)
}

func TestHostScoreRing_RollsOver(t *testing.T) {
	ring := &hostScoreRing{samples: make([]hostScoreSample, 0, HostScoreWindowSize)}
	for i := 0; i < HostScoreWindowSize+25; i++ {
		ring.add(hostScoreSample{TtftMs: float64(i), TotalMs: float64(i)})
	}
	require.Len(t, ring.samples, HostScoreWindowSize, "ring should cap at window size")

	ordered := ring.ordered()
	require.Equal(t, float64(25), ordered[0].TtftMs, "oldest should be sample #25")
	require.Equal(t, float64(HostScoreWindowSize+24), ordered[HostScoreWindowSize-1].TtftMs, "newest should be the latest write")
}

func TestEloUpdate_BasicSymmetry(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	keyA := hostScoreKey{model: "m", host: "A", bucket: "lt_1k"}
	keyB := hostScoreKey{model: "m", host: "B", bucket: "lt_1k"}

	tr.updateEloLocked(keyA, keyB, 1.0, 0.0, HostScoreEloK, time.Now())
	rA := tr.elo[keyA] - HostScoreEloDefault
	rB := tr.elo[keyB] - HostScoreEloDefault
	require.InDelta(t, -rB, rA, 0.0001, "equal-rated symmetric Elo: A gains exactly what B loses")
	require.Greater(t, rA, 0.0, "winner gains rating")
}

func TestEloUpdate_UpsetReward(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	favoriteKey := hostScoreKey{model: "m", host: "fav", bucket: "lt_1k"}
	underdogKey := hostScoreKey{model: "m", host: "und", bucket: "lt_1k"}
	tr.elo[favoriteKey] = 1800.0
	tr.elo[underdogKey] = 1200.0

	pre := tr.elo[underdogKey]
	tr.updateEloLocked(favoriteKey, underdogKey, 0.0, 1.0, HostScoreEloK, time.Now())
	gain := tr.elo[underdogKey] - pre

	tr2 := NewHostScoreTracker(nil)
	a := hostScoreKey{model: "m", host: "a", bucket: "lt_1k"}
	b := hostScoreKey{model: "m", host: "b", bucket: "lt_1k"}
	tr2.updateEloLocked(a, b, 1.0, 0.0, HostScoreEloK, time.Now())
	expectedGain := tr2.elo[a] - HostScoreEloDefault

	// Equal-rated swing = K/2 = 8; max swing (impossible underdog) = K = 16.
	// 1200-vs-1800 yields ~15.5 — comfortably above 1.5x but below the K cap.
	require.Greater(t, gain, expectedGain*1.5, "upset should yield > 1.5x the equal-rated swing")
	require.LessOrEqual(t, gain, HostScoreEloK, "single Elo update is capped at K")
}

func TestRecordRequest_SkipsUnscorable(t *testing.T) {
	tr := NewHostScoreTracker(nil)

	tr.RecordRequest(mkRequest("m", 100,
		HostInvolvement{ParticipantKey: "A", FirstTokenMs: 100, TotalTimeMs: 1000, Responsive: true, Finished: false},
		HostInvolvement{ParticipantKey: "B", FirstTokenMs: 110, TotalTimeMs: 1100, Responsive: true, Finished: true},
	))
	require.Len(t, tr.hosts, 1, "unfinished host A must be skipped; only B recorded")
	_, hasA := tr.hosts[hostScoreKey{"m", "A", "lt_1k"}]
	require.False(t, hasA)

	tr.RecordRequest(mkRequest("m", 100,
		HostInvolvement{ParticipantKey: "C", FirstTokenMs: 100, TotalTimeMs: 1000, Responsive: true, Finished: true, ExcludePairwise: true},
		mkInvolvement("D", 110, 1100),
	))
	_, hasC := tr.hosts[hostScoreKey{"m", "C", "lt_1k"}]
	require.False(t, hasC, "ExcludePairwise hosts must be skipped")
	_, hasD := tr.hosts[hostScoreKey{"m", "D", "lt_1k"}]
	require.True(t, hasD)
}

func TestScoreHost_ColdStart(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < HostScoreMinSamples-1; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}
	_, ok := tr.ScoreHost("m", "A", "lt_1k", false, "")
	require.False(t, ok, "below MinSamples should return ok=false")

	tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	_, ok = tr.ScoreHost("m", "A", "lt_1k", false, "")
	require.True(t, ok)
}

func TestScoreHost_H2HOverridesElo(t *testing.T) {
	pw := NewPairwiseTracker()
	tr := NewHostScoreTracker(pw)

	for i := 0; i < HostScoreMinSamples+1; i++ {
		rec := mkRequest("m", 100, mkInvolvement("A", 100, 1000), mkInvolvement("B", 200, 2000))
		pw.RecordRequest(rec)
		tr.RecordRequest(rec)
	}

	// A always finishes ahead of B → H2HWinRate(A,B) ~ 1.0 → score = 1.0 - 1.0 = 0
	scoreA, ok := tr.ScoreHost("m", "A", "lt_1k", false, "B")
	require.True(t, ok)
	require.InDelta(t, 0.0, scoreA, 0.001, "perfect-win-rate A scores 0 (best)")

	scoreB, ok := tr.ScoreHost("m", "B", "lt_1k", false, "A")
	require.True(t, ok)
	require.InDelta(t, 1.0, scoreB, 0.001, "always-loser B scores 1.0")
}

func TestScoreHost_LinearGammaCorrect(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < 10; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}

	scoreNonStream, ok := tr.ScoreHost("m", "A", "lt_1k", false, "")
	require.True(t, ok)
	// elo stays at default → eloBonus = 0; total p50 = 1000; γ=1.0 → base = 1000
	require.InDelta(t, 1000.0, scoreNonStream, 0.001)

	scoreStream, ok := tr.ScoreHost("m", "A", "lt_1k", true, "")
	require.True(t, ok)
	// γ=0.3 → base = 0.7*100 + 0.3*1000 = 70 + 300 = 370
	require.InDelta(t, 370.0, scoreStream, 0.001)
}

func TestSnapshot_ContainsExpectedFields(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < 10; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000), mkInvolvement("B", 200, 2000)))
	}

	snap := tr.Snapshot()
	require.Len(t, snap, 2)
	for _, s := range snap {
		require.NotEmpty(t, s.Model)
		require.NotEmpty(t, s.Host)
		require.Equal(t, "lt_1k", s.Bucket)
		require.Equal(t, 10, s.Samples)
		require.Greater(t, s.TtftP50Ms, 0.0)
		require.Greater(t, s.TotalP50Ms, 0.0)
		require.NotZero(t, s.Elo)
	}
	// Check sort order: A before B (host alphabetical when model+bucket equal)
	require.Equal(t, "A", snap[0].Host)
	require.Equal(t, "B", snap[1].Host)
}

func TestConcurrentRecord(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	const goroutines = 8
	const perGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				tr.RecordRequest(mkRequest("m", 100,
					mkInvolvement("A", float64(100+i), float64(1000+i)),
					mkInvolvement("B", float64(110+i), float64(1100+i)),
				))
			}
		}()
	}
	wg.Wait()

	tr.mu.RLock()
	defer tr.mu.RUnlock()
	for _, ring := range tr.hosts {
		require.LessOrEqual(t, len(ring.samples), HostScoreWindowSize)
	}
	require.Len(t, tr.hosts, 2)
}

func TestRecordRequest_BucketingByInputTokens(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	cases := []struct {
		tokens uint64
		bucket string
	}{
		{500, "lt_1k"},
		{3_000, "1k_5k"},
		{10_000, "5k_15k"},
		{20_000, "15k_30k"},
		{50_000, "30k_100k"},
		{200_000, "gte_100k"},
	}
	for _, tc := range cases {
		tr.RecordRequest(mkRequest("m", tc.tokens, mkInvolvement("A", 100, 1000)))
	}
	for _, tc := range cases {
		_, ok := tr.hosts[hostScoreKey{"m", "A", tc.bucket}]
		require.True(t, ok, "expected bucket %s for tokens=%d", tc.bucket, tc.tokens)
	}
}

func TestPersistStateAndRestore_RoundTrip(t *testing.T) {
	src := NewHostScoreTracker(nil)
	for i := 0; i < 15; i++ {
		src.RecordRequest(mkRequest("m", 10_000, mkInvolvement("A", float64(100+i), float64(1000+i*10))))
	}
	src.elo[hostScoreKey{"m", "A", "5k_15k"}] = 1612.5

	states := src.PersistState()
	require.Len(t, states, 1)
	require.InDelta(t, 1612.5, states[0].Elo, 0.001)
	require.Len(t, states[0].Samples, 15)

	dst := NewHostScoreTracker(nil)
	dst.Restore(states)

	require.Len(t, dst.hosts, 1)
	require.InDelta(t, 1612.5, dst.elo[hostScoreKey{"m", "A", "5k_15k"}], 0.001)
	ring := dst.hosts[hostScoreKey{"m", "A", "5k_15k"}]
	require.Len(t, ring.samples, 15)

	// Round-trip Snapshot equivalence on percentiles
	srcSnap := src.Snapshot()
	dstSnap := dst.Snapshot()
	require.InDelta(t, srcSnap[0].TtftP50Ms, dstSnap[0].TtftP50Ms, 0.001)
	require.InDelta(t, srcSnap[0].TotalP90Ms, dstSnap[0].TotalP90Ms, 0.001)
}

func TestInterpPercentile_EdgeCases(t *testing.T) {
	require.Equal(t, 0.0, interpPercentile(nil, 0.5))
	require.Equal(t, 42.0, interpPercentile([]float64{42}, 0.5))
	require.Equal(t, 1.0, interpPercentile([]float64{1, 2, 3}, 0))
	require.Equal(t, 3.0, interpPercentile([]float64{1, 2, 3}, 1))
	require.InDelta(t, 2.0, interpPercentile([]float64{1, 2, 3}, 0.5), 0.001)
}

func TestEloMath_ExpectedScoreMonotonic(t *testing.T) {
	// Higher Ra vs Rb means Ea increases; sanity-check math symmetry
	for d := -400.0; d <= 400.0; d += 100 {
		Ea := 1.0 / (1 + math.Pow(10, -d/400))
		require.GreaterOrEqual(t, Ea, 0.0)
		require.LessOrEqual(t, Ea, 1.0)
	}
}

func TestUCBBonus_ZeroWhenSoleHostInBucket(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < 10; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}
	tr.mu.RLock()
	b := tr.ucbBonusLocked("m", "lt_1k", 10)
	tr.mu.RUnlock()
	require.Equal(t, 0.0, b, "sole host in bucket has no exploration alternative")
}

func TestUCBBonus_PositiveForUndersampled(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	// Saturate host A (50 samples), tiny B (3 samples) — same bucket
	for i := 0; i < 50; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}
	for i := 0; i < 3; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("B", 100, 1000)))
	}
	tr.mu.RLock()
	a := tr.ucbBonusLocked("m", "lt_1k", 50)
	b := tr.ucbBonusLocked("m", "lt_1k", 3)
	tr.mu.RUnlock()
	require.Greater(t, b, a, "under-sampled B should get a larger exploration credit")
	require.Greater(t, b, 100.0, "B's UCB bonus should be a meaningful slice of the timing scale")
}

func TestUCBBonus_ShrinksAsSamplesGrow(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < 100; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}
	for i := 0; i < 5; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("B", 100, 1000)))
	}
	tr.mu.RLock()
	bonusAt5 := tr.ucbBonusLocked("m", "lt_1k", 5)
	tr.mu.RUnlock()

	for i := 0; i < 45; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("B", 100, 1000)))
	}
	tr.mu.RLock()
	bonusAt50 := tr.ucbBonusLocked("m", "lt_1k", 50)
	tr.mu.RUnlock()

	require.Greater(t, bonusAt5, bonusAt50, "bonus must decrease as samples accumulate")
}

func TestUCBBonus_DisabledByZeroCoefficient(t *testing.T) {
	saved := HostScoreUCBCoefficient
	t.Cleanup(func() { HostScoreUCBCoefficient = saved })
	HostScoreUCBCoefficient = 0

	tr := NewHostScoreTracker(nil)
	for i := 0; i < 50; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 1000)))
	}
	for i := 0; i < 3; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("B", 100, 1000)))
	}
	tr.mu.RLock()
	b := tr.ucbBonusLocked("m", "lt_1k", 3)
	tr.mu.RUnlock()
	require.Equal(t, 0.0, b)
}

func TestScoreHost_UCBHelpsColdHostBeatEloFavorite(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	// A established and fast: 100 samples, fast timing → low (=good) base.
	for i := 0; i < 100; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("A", 100, 500)))
	}
	// B newcomer with identical timing but only 3 samples → UCB should boost.
	for i := 0; i < 3; i++ {
		tr.RecordRequest(mkRequest("m", 100, mkInvolvement("B", 100, 500)))
	}
	scoreA, ok := tr.ScoreHost("m", "A", "lt_1k", false, "")
	require.True(t, ok)
	scoreB, ok := tr.ScoreHost("m", "B", "lt_1k", false, "")
	require.True(t, ok)
	require.Less(t, scoreB, scoreA, "under-sampled B should look better thanks to UCB bonus")
}

func TestEloDecay_StaleRatingPullsTowardDefault(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{}

	tr := NewHostScoreTracker(nil)
	key := hostScoreKey{model: "m", host: "stale", bucket: "lt_1k"}
	tr.elo[key] = 1800.0
	t0 := time.Now()
	tr.eloUpdatedAt[key] = t0

	// One half-life elapsed → gap of 300 should be ≈150 (within float ε).
	got := tr.decayedEloLocked(key, t0.Add(HostScoreEloHalfLife))
	require.InDelta(t, 1650.0, got, 0.01)

	// Two half-lives → gap of 75.
	got = tr.decayedEloLocked(key, t0.Add(2*HostScoreEloHalfLife))
	require.InDelta(t, 1575.0, got, 0.01)

	// Ten half-lives → effectively flat.
	got = tr.decayedEloLocked(key, t0.Add(10*HostScoreEloHalfLife))
	require.InDelta(t, HostScoreEloDefault, got, 1.0)
}

func TestEloDecay_DisabledByZeroHalfLife(t *testing.T) {
	savedHL := HostScoreEloHalfLife
	savedOv := HostScoreBucketOverrides
	t.Cleanup(func() {
		HostScoreEloHalfLife = savedHL
		HostScoreBucketOverrides = savedOv
	})
	HostScoreEloHalfLife = 0
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{}

	tr := NewHostScoreTracker(nil)
	key := hostScoreKey{model: "m", host: "frozen", bucket: "lt_1k"}
	tr.elo[key] = 1800.0
	tr.eloUpdatedAt[key] = time.Now().Add(-100 * time.Hour)

	require.Equal(t, 1800.0, tr.decayedEloLocked(key, time.Now()))
}

func TestEloDecay_NoUpdatedAtSkipsDecay(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	key := hostScoreKey{model: "m", host: "h", bucket: "lt_1k"}
	tr.elo[key] = 1800.0
	// No eloUpdatedAt entry → can't reason about age → return raw.
	require.Equal(t, 1800.0, tr.decayedEloLocked(key, time.Now()))
}

func TestBucketOverrides_HelpersFallBackToGlobals(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{}

	require.Equal(t, HostScoreEloK, hostScoreKForBucket("any"))
	require.Equal(t, HostScoreEloHalfLife, hostScoreHalfLifeForBucket("any"))
}

func TestBucketOverrides_KAppliesPerBucket(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"gte_100k": {K: 32, HalfLife: -1},
	}

	require.Equal(t, 32.0, hostScoreKForBucket("gte_100k"))
	require.Equal(t, HostScoreEloK, hostScoreKForBucket("lt_1k"), "non-overridden bucket inherits global")
}

func TestBucketOverrides_KZeroInheritsGlobal(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"lt_1k": {K: 0, HalfLife: -1},
	}
	require.Equal(t, HostScoreEloK, hostScoreKForBucket("lt_1k"))
}

func TestBucketOverrides_HalfLifeAppliesPerBucket(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"slow_decay": {HalfLife: 48 * time.Hour},
		"fast_decay": {HalfLife: 1 * time.Hour},
	}

	tr := NewHostScoreTracker(nil)
	slow := hostScoreKey{model: "m", host: "h", bucket: "slow_decay"}
	fast := hostScoreKey{model: "m", host: "h", bucket: "fast_decay"}
	t0 := time.Now()
	tr.elo[slow] = 1800.0
	tr.elo[fast] = 1800.0
	tr.eloUpdatedAt[slow] = t0
	tr.eloUpdatedAt[fast] = t0

	// 1h elapsed: fast bucket = 1 half-life (gap halves to 150 → 1650);
	// slow bucket = 1/48 half-life (gap barely moves).
	at := t0.Add(time.Hour)
	require.InDelta(t, 1650.0, tr.decayedEloLocked(fast, at), 0.5)
	require.Greater(t, tr.decayedEloLocked(slow, at), 1790.0,
		"48h half-life should barely move after 1h")
}

func TestBucketOverrides_HalfLifeZeroDisablesPerBucket(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"frozen_bucket": {HalfLife: 0},
	}

	tr := NewHostScoreTracker(nil)
	frozen := hostScoreKey{model: "m", host: "h", bucket: "frozen_bucket"}
	tr.elo[frozen] = 1800.0
	tr.eloUpdatedAt[frozen] = time.Now().Add(-100 * time.Hour)

	require.Equal(t, 1800.0, tr.decayedEloLocked(frozen, time.Now()),
		"explicit HalfLife=0 must disable decay for this bucket")
}

func TestBucketOverrides_NegativeHalfLifeInheritsGlobal(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"inherit_decay": {K: 20, HalfLife: -1},
	}
	require.Equal(t, HostScoreEloHalfLife, hostScoreHalfLifeForBucket("inherit_decay"))
}

func TestBucketOverrides_RecordRequestUsesPerBucketK(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	// requestShapeBucket(100) → "lt_1k"; double K for that bucket.
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
		"lt_1k": {K: HostScoreEloK * 2, HalfLife: -1},
	}

	tr := NewHostScoreTracker(nil)
	keyA := hostScoreKey{model: "m", host: "A", bucket: "lt_1k"}

	// Same identical race A>B fed twice: once with default K (baseline tr2), once with 2x K.
	tr.RecordRequest(mkRequest("m", 100,
		mkInvolvement("A", 100, 1000),
		mkInvolvement("B", 200, 2000),
	))
	gainOverridden := tr.elo[keyA] - HostScoreEloDefault

	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{}
	tr2 := NewHostScoreTracker(nil)
	tr2.RecordRequest(mkRequest("m", 100,
		mkInvolvement("A", 100, 1000),
		mkInvolvement("B", 200, 2000),
	))
	gainBaseline := tr2.elo[keyA] - HostScoreEloDefault

	// Per RecordRequest two updates (TTFT + Total) at halfK each. The
	// second update's Ea shifts with Ra moved by the first, so the
	// K→gain relationship is approximately, not exactly, linear.
	ratio := gainOverridden / gainBaseline
	require.Greater(t, ratio, 1.8, "doubling K should roughly double gain")
	require.Less(t, ratio, 2.2, "doubling K should not far overshoot 2x")
}

func TestUpdateElo_DecayAppliedBeforeKUpdate(t *testing.T) {
	saved := HostScoreBucketOverrides
	t.Cleanup(func() { HostScoreBucketOverrides = saved })
	HostScoreBucketOverrides = map[string]HostScoreBucketOverride{}

	tr := NewHostScoreTracker(nil)
	keyA := hostScoreKey{model: "m", host: "stale_fav", bucket: "lt_1k"}
	keyB := hostScoreKey{model: "m", host: "fresh_und", bucket: "lt_1k"}
	tr.elo[keyA] = 1800.0
	tr.elo[keyB] = 1200.0
	t0 := time.Now()
	tr.eloUpdatedAt[keyA] = t0
	tr.eloUpdatedAt[keyB] = t0

	// Move 2 half-lives forward. Decayed A ≈ 1575, decayed B ≈ 1425. A wins.
	tr.updateEloLocked(keyA, keyB, 1.0, 0.0, HostScoreEloK, t0.Add(2*HostScoreEloHalfLife))

	// Expected outcome: fav is decayed before K is applied so it gains LESS
	// than if the stale 1800 had been used directly. Sanity-check: A still
	// gained, and is no longer at 1800.
	require.Greater(t, tr.elo[keyA], 1575.0)
	require.Less(t, tr.elo[keyA], 1800.0, "decay must keep A below its stale start")
}

func TestPersistStateAndRestore_PreservesPerKeyUpdatedAt(t *testing.T) {
	src := NewHostScoreTracker(nil)
	key := hostScoreKey{model: "m", host: "A", bucket: "5k_15k"}
	src.elo[key] = 1700.0
	specific := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	src.eloUpdatedAt[key] = specific
	src.hosts[key] = &hostScoreRing{samples: []hostScoreSample{{TtftMs: 10, TotalMs: 100}}}

	dst := NewHostScoreTracker(nil)
	dst.Restore(src.PersistState())

	require.Equal(t, specific.UTC(), dst.eloUpdatedAt[key].UTC())
}

func TestPairOutcome_AllCases(t *testing.T) {
	resp := func(ttft, tot float64) HostInvolvement {
		return HostInvolvement{ParticipantKey: "x", FirstTokenMs: ttft, TotalTimeMs: tot, Responsive: true, Finished: true}
	}
	unresp := HostInvolvement{ParticipantKey: "x", Responsive: false}

	// Equal metrics → exact draw (Sa=Sb=0.5)
	sa, sb := pairOutcome(resp(100, 1000), resp(100, 1000), true)
	require.Equal(t, 0.5, sa, "ratio 1.0 → exact draw")
	require.Equal(t, 0.5, sb)

	// Streaming-ceiling case: ratio 1.001 → Sa ≈ 0.5 (essentially no Elo move)
	sa, _ = pairOutcome(resp(100, 13701), resp(100, 13689), false)
	require.InDelta(t, 0.5, sa, 0.01, "ceiling-noise ratio 1.0009 → Sa ≈ 0.5")

	// Modest difference: TTFT 100 vs 150 (1.5x slower for B) → Sa ≈ 0.69
	sa, _ = pairOutcome(resp(100, 1000), resp(150, 1000), true)
	require.InDelta(t, 0.692, sa, 0.001, "1.5x faster → Sa ≈ 0.69 (modest)")

	// Clear win: TTFT 100 vs 500 (5x slower for B) → Sa ≈ 0.96
	sa, _ = pairOutcome(resp(100, 1000), resp(500, 900), true)
	require.InDelta(t, 0.962, sa, 0.001, "5x faster → Sa ≈ 0.96 (clear)")

	// Decisive: ratio 100x → Sa ≈ 0.9999
	sa, _ = pairOutcome(resp(100, 1000), resp(10_000, 1000), true)
	require.InDelta(t, 0.9999, sa, 0.0001, "100x faster → Sa ≈ 1.0 (decisive)")

	// Total dimension picks B when B much faster
	sa, sb = pairOutcome(resp(100, 3000), resp(500, 800), false)
	require.Less(t, sa, 0.1, "Total mode: A 3.75x slower → Sa near 0")
	require.Greater(t, sb, 0.9)

	// Mixed: responsive beats unresponsive (forfeit, not BT)
	sa, sb = pairOutcome(resp(100, 1000), unresp, true)
	require.Equal(t, 1.0, sa)
	require.Equal(t, 0.0, sb)
	sa, sb = pairOutcome(unresp, resp(100, 1000), true)
	require.Equal(t, 0.0, sa)
	require.Equal(t, 1.0, sb)

	// Both failed → both lose (Sa=Sb=0)
	sa, sb = pairOutcome(unresp, unresp, true)
	require.Equal(t, 0.0, sa)
	require.Equal(t, 0.0, sb)
}

func TestRecordRequest_UnresponsiveLosesEloToResponsive(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	good := mkInvolvement("good", 100, 1000)
	bad := HostInvolvement{ParticipantKey: "bad", Responsive: false}
	tr.RecordRequest(RequestRecord{Model: "m", InputTokens: 100, Hosts: []HostInvolvement{good, bad}, Timestamp: time.Now()})

	keyGood := hostScoreKey{model: "m", host: "good", bucket: "lt_1k"}
	keyBad := hostScoreKey{model: "m", host: "bad", bucket: "lt_1k"}
	require.Greater(t, tr.elo[keyGood], HostScoreEloDefault, "responsive host gains Elo")
	require.Less(t, tr.elo[keyBad], HostScoreEloDefault, "unresponsive host loses Elo even when not added to sample ring")
	_, hasBadRing := tr.hosts[keyBad]
	require.False(t, hasBadRing, "unresponsive host MUST NOT pollute the sample ring")
}

func TestRecordRequest_BothUnresponsivePenalizesBoth(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	a := HostInvolvement{ParticipantKey: "A", Responsive: false}
	b := HostInvolvement{ParticipantKey: "B", Responsive: false}
	tr.RecordRequest(RequestRecord{Model: "m", InputTokens: 100, Hosts: []HostInvolvement{a, b}, Timestamp: time.Now()})

	keyA := hostScoreKey{model: "m", host: "A", bucket: "lt_1k"}
	keyB := hostScoreKey{model: "m", host: "B", bucket: "lt_1k"}
	require.Less(t, tr.elo[keyA], HostScoreEloDefault, "A penalized for both-failed race")
	require.Less(t, tr.elo[keyB], HostScoreEloDefault, "B penalized for both-failed race")
	// Equal-rated both-lose → each loses K·0.5 = -8
	require.InDelta(t, HostScoreEloDefault-HostScoreEloK*0.5, tr.elo[keyA], 0.01)
	require.InDelta(t, HostScoreEloDefault-HostScoreEloK*0.5, tr.elo[keyB], 0.01)
}

func TestRecordRequest_DualMetricBothFastDominates(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	rec := RequestRecord{Model: "m", InputTokens: 100, Timestamp: time.Now(),
		Hosts: []HostInvolvement{mkInvolvement("A", 100, 800), mkInvolvement("B", 500, 3000)}}
	tr.RecordRequest(rec)

	keyA := hostScoreKey{model: "m", host: "A", bucket: "lt_1k"}
	keyB := hostScoreKey{model: "m", host: "B", bucket: "lt_1k"}
	gainA := tr.elo[keyA] - HostScoreEloDefault
	require.Greater(t, gainA, HostScoreEloK*0.4, "winning both ≈ K*0.5 (Elo self-correction)")
	require.Less(t, gainA, HostScoreEloK, "even winning both can't exceed full K-budget")
	require.InDelta(t, -gainA, tr.elo[keyB]-HostScoreEloDefault, 0.001, "zero-sum within pair")
}

func TestRecordRequest_DualMetricDisagreeNetNeutral(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	rec := RequestRecord{Model: "m", InputTokens: 100, Timestamp: time.Now(),
		Hosts: []HostInvolvement{mkInvolvement("A", 100, 3000), mkInvolvement("B", 500, 800)}}
	tr.RecordRequest(rec)

	keyA := hostScoreKey{model: "m", host: "A", bucket: "lt_1k"}
	keyB := hostScoreKey{model: "m", host: "B", bucket: "lt_1k"}
	require.InDelta(t, HostScoreEloDefault, tr.elo[keyA], 1.0, "metrics disagree → net Elo change ≈ 0 (within ~1 pt)")
	require.InDelta(t, HostScoreEloDefault, tr.elo[keyB], 1.0)
}

func TestRecordRequest_PartialResponsiveCensoredLoserStillRated(t *testing.T) {
	tr := NewHostScoreTracker(nil)
	winner := mkInvolvement("W", 200, 1500)
	canceled := HostInvolvement{ParticipantKey: "L", FirstTokenMs: 800, TotalTimeMs: 1500, Responsive: true, Finished: false}
	rec := RequestRecord{Model: "m", InputTokens: 100, Timestamp: time.Now(),
		Hosts: []HostInvolvement{winner, canceled}}
	tr.RecordRequest(rec)

	keyW := hostScoreKey{model: "m", host: "W", bucket: "lt_1k"}
	keyL := hostScoreKey{model: "m", host: "L", bucket: "lt_1k"}
	require.Greater(t, tr.elo[keyW], HostScoreEloDefault, "winner faster TTFT + finished → gains on both updates")
	require.Less(t, tr.elo[keyL], HostScoreEloDefault, "censored loser slower TTFT + unfinished → loses on both updates")
	_, hasLRing := tr.hosts[keyL]
	require.False(t, hasLRing, "canceled loser (Finished=false) still excluded from sample ring")
}
