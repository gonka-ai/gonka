package semanticcache

import (
	"context"
	"testing"
)

// ── CosineBps ─────────────────────────────────────────────────────────────────

// TestCosineBps_Identical verifies that identical vectors return 10000 bps (100%).
func TestCosineBps_Identical(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	score := CosineBps(a, a)
	if score != 10000 {
		t.Errorf("identical vectors: expected 10000, got %d", score)
	}
}

// TestCosineBps_Orthogonal verifies that orthogonal vectors return 0 bps.
func TestCosineBps_Orthogonal(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	score := CosineBps(a, b)
	if score != 0 {
		t.Errorf("orthogonal vectors: expected 0, got %d", score)
	}
}

// TestCosineBps_Similar verifies a partial similarity value.
// Vectors {1,1,0} and {1,0,0} have cosine = 1/sqrt(2) ≈ 0.7071 → 7071 bps.
func TestCosineBps_Similar(t *testing.T) {
	a := []float32{1.0, 1.0, 0.0}
	b := []float32{1.0, 0.0, 0.0}
	score := CosineBps(a, b)
	// cos = 1/sqrt(2) ≈ 7071 bps
	if score < 7000 || score > 7200 {
		t.Errorf("expected ~7071 bps for cosine(a,b), got %d", score)
	}
}

// TestCosineBps_ZeroVector returns 0 for a zero vector (no divide-by-zero panic).
func TestCosineBps_ZeroVector(t *testing.T) {
	zero := []float32{0.0, 0.0, 0.0}
	any := []float32{1.0, 0.0, 0.0}
	if s := CosineBps(zero, any); s != 0 {
		t.Errorf("zero vector: expected 0, got %d", s)
	}
}

// TestCosineBps_LengthMismatch returns 0 for incompatible vectors (no panic).
func TestCosineBps_LengthMismatch(t *testing.T) {
	a := []float32{1.0, 0.0}
	b := []float32{1.0, 0.0, 0.0}
	if s := CosineBps(a, b); s != 0 {
		t.Errorf("length mismatch: expected 0, got %d", s)
	}
}

// ── EmbedText ─────────────────────────────────────────────────────────────────

// TestEmbedText_DelegatesEmbedder verifies that EmbedText calls the underlying
// embedder and returns the same vector.
func TestEmbedText_DelegatesEmbedder(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)
	vec, err := sc.EmbedText(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatalf("EmbedText failed: %v", err)
	}
	// fixedEmbedder always returns {1,0,0}
	if len(vec) != 3 || vec[0] != 1.0 {
		t.Errorf("unexpected vector from EmbedText: %v", vec)
	}
}

// ── RecordCoherenceResult / CoherenceStats ────────────────────────────────────

// TestCoherenceStats_AcceptedAccumulates verifies that accepted results
// accumulate in coherenceSumBps and are counted in contextHitCount.
func TestCoherenceStats_AcceptedAccumulates(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)

	sc.RecordCoherenceResult(7000, true)
	sc.RecordCoherenceResult(8000, true)

	hits, rejections, sumBps := sc.CoherenceStats()
	if hits != 2 {
		t.Errorf("expected contextHitCount=2, got %d", hits)
	}
	if rejections != 0 {
		t.Errorf("expected rejections=0, got %d", rejections)
	}
	if sumBps != 15000 {
		t.Errorf("expected coherenceSumBps=15000, got %d", sumBps)
	}
}

// TestCoherenceStats_RejectedCountsCorrectly verifies that rejected results
// increment rejections but do not add to coherenceSumBps.
func TestCoherenceStats_RejectedCountsCorrectly(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)

	sc.RecordCoherenceResult(2000, false) // below floor, rejected
	sc.RecordCoherenceResult(7500, true)  // accepted

	hits, rejections, sumBps := sc.CoherenceStats()
	if hits != 2 {
		t.Errorf("expected contextHitCount=2, got %d", hits)
	}
	if rejections != 1 {
		t.Errorf("expected rejections=1, got %d", rejections)
	}
	// Only accepted score (7500) contributes to sum
	if sumBps != 7500 {
		t.Errorf("expected coherenceSumBps=7500 (only accepted), got %d", sumBps)
	}
}

// TestCoherenceStats_AvgCoherenceCalculation verifies the avg_coherence_bps
// formula used in getCacheStats:  sum / (contextHits - rejections)
func TestCoherenceStats_AvgCoherenceCalculation(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)

	sc.RecordCoherenceResult(6000, true)
	sc.RecordCoherenceResult(8000, true)
	sc.RecordCoherenceResult(1000, false) // rejected

	hits, rejections, sumBps := sc.CoherenceStats()
	accepted := hits - rejections // 2
	if accepted <= 0 {
		t.Fatal("expected accepted > 0")
	}
	avg := sumBps / accepted // (6000+8000)/2 = 7000
	if avg != 7000 {
		t.Errorf("expected avg coherence = 7000, got %d", avg)
	}
}

// TestCoherenceStats_ZeroOnStart verifies all counters start at zero.
func TestCoherenceStats_ZeroOnStart(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)
	hits, rejections, sumBps := sc.CoherenceStats()
	if hits != 0 || rejections != 0 || sumBps != 0 {
		t.Errorf("expected all zeroes at start, got hits=%d rejections=%d sum=%d",
			hits, rejections, sumBps)
	}
}

// ── CoherenceScoreBps field in CachedResult ───────────────────────────────────

// TestCachedResult_CoherenceScoreBps verifies that CoherenceScoreBps is stored
// and retrieved correctly through Store/LookupExact.
func TestCachedResult_CoherenceScoreBps(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{
		PromptHash:        "abc",
		ModelVersion:      "v1",
		ValidUntilEpoch:   100,
		CoherenceScoreBps: 6840,
	}
	_ = store.Store(context.Background(), []float32{1, 0, 0, 0}, result)

	r, hit := store.LookupExact("abc")
	if !hit {
		t.Fatal("expected L1 hit")
	}
	if r.CoherenceScoreBps != 6840 {
		t.Errorf("expected CoherenceScoreBps=6840, got %d", r.CoherenceScoreBps)
	}
}
