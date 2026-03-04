package semanticcache

import (
	"context"
	"testing"
)

// ── deterministic in-memory store for tests ───────────────────────────────────

type memStore struct {
	result CachedResult
	hit    bool
}

func (m *memStore) Lookup(_ context.Context, _ []float32, _ uint32) (CachedResult, bool) {
	return m.result, m.hit
}
func (m *memStore) Store(_ context.Context, _ []float32, r CachedResult) error {
	m.result = r
	m.hit = true
	return nil
}
func (m *memStore) RecordReuse(_ context.Context, _ string, _ uint64, _ uint32) error {
	return nil
}
func (m *memStore) EvictExpired(_ context.Context, _ uint64) error { return nil }

// fixedEmbedder always returns a fixed 3-dimensional vector
type fixedEmbedder struct{}

func (fixedEmbedder) Embed(_ context.Context, _ []byte) ([]float32, error) {
	return []float32{1.0, 0.0, 0.0}, nil
}
func (fixedEmbedder) Dimensions() int { return 3 }

func newTestCache(modelVersion string, maxAge uint64) (*SemanticCache, *memStore) {
	store := &memStore{}
	sc := NewSemanticCache(fixedEmbedder{}, store, 9700, modelVersion, maxAge, true)
	return sc, store
}

// ── TTL layer (Layer 2) ───────────────────────────────────────────────────────

// TestTTL_ExpiredResult verifies that a result whose ValidUntilEpoch < currentEpoch
// is served as a cache miss even though similarity and model version match.
//
// Proof: this is the off-chain TTL enforcement documented in
// CacheQualityParams.MaxCacheAgeEpochs.  Without this check, vectors stored
// during epoch N would remain valid and accumulate indefinitely.
func TestTTL_ExpiredResult(t *testing.T) {
	sc, store := newTestCache("v1", 10)
	store.hit = true
	store.result = CachedResult{
		ModelVersion:    "v1",
		ValidUntilEpoch: 5, // stored at epoch 0 with maxAge=5 → expired
	}

	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 6) // current epoch=6 > 5
	if hit {
		t.Fatal("expected cache miss for expired result (ValidUntilEpoch=5 < currentEpoch=6), got hit")
	}
	if sc.hitCount != 0 {
		t.Fatalf("expected hitCount=0, got %d", sc.hitCount)
	}
}

// TestTTL_ValidResult verifies that a result still within TTL is served.
func TestTTL_ValidResult(t *testing.T) {
	sc, store := newTestCache("v1", 10)
	store.hit = true
	store.result = CachedResult{
		ModelVersion:    "v1",
		ValidUntilEpoch: 10,
	}

	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 10) // at boundary
	if !hit {
		t.Fatal("expected cache hit for result at ValidUntilEpoch boundary, got miss")
	}
}

// ── Model version layer (Layer 3) ─────────────────────────────────────────────

// TestModelVersion_Mismatch verifies that a result from a different embedding
// model is rejected even when within TTL and similarity threshold.
//
// Proof: this is the "Rosetta Stone" problem — all-MiniLM-L6-v2 v1 and v2
// produce incomparable cosine similarity scores for identical prompts.
// Without this check, a node upgraded to v2 would serve v1 cached results
// with invalid (incomparable) similarity claims.
func TestModelVersion_Mismatch(t *testing.T) {
	sc, store := newTestCache("v2", 10) // governance now requires v2
	store.hit = true
	store.result = CachedResult{
		ModelVersion:    "v1", // stored with old model
		ValidUntilEpoch: 100,
	}

	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 5)
	if hit {
		t.Fatal("expected cache miss for model version mismatch (v1 stored, v2 required), got hit")
	}
}

// TestModelVersion_Match verifies that a result from the correct model version is served.
func TestModelVersion_Match(t *testing.T) {
	sc, store := newTestCache("v2", 10)
	store.hit = true
	store.result = CachedResult{
		ModelVersion:    "v2",
		ValidUntilEpoch: 100,
	}

	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 5)
	if !hit {
		t.Fatal("expected cache hit for matching model version, got miss")
	}
}

// ── StoreResult sets TTL and model version ────────────────────────────────────

// TestStoreResult_SetsModelVersionAndTTL verifies that StoreResult correctly
// populates ModelVersion and ValidUntilEpoch from governance parameters.
//
// Proof: without this, a node could call StoreResult with empty ModelVersion
// and a result would pass model version checks incorrectly on subsequent nodes.
func TestStoreResult_SetsModelVersionAndTTL(t *testing.T) {
	sc, store := newTestCache("v1", 10)

	result := CachedResult{
		PromptHash:                 "abc",
		OriginalParticipantAddress: "cosmos1xxx",
	}

	err := sc.StoreResult(context.Background(), []byte("prompt"), nil, result, 5)
	if err != nil {
		t.Fatalf("StoreResult failed: %v", err)
	}

	stored := store.result
	if stored.ModelVersion != "v1" {
		t.Errorf("expected ModelVersion='v1', got %q", stored.ModelVersion)
	}
	if stored.OriginalEpoch != 5 {
		t.Errorf("expected OriginalEpoch=5, got %d", stored.OriginalEpoch)
	}
	if stored.ValidUntilEpoch != 15 { // 5 + maxAge(10)
		t.Errorf("expected ValidUntilEpoch=15 (5+10), got %d", stored.ValidUntilEpoch)
	}
}

// ── UpdateCacheParams live governance update ──────────────────────────────────

// TestUpdateCacheParams_ModelVersionChange verifies that changing the governance
// model version immediately invalidates previously cached results without any
// manual Qdrant cleanup.
//
// Proof: this is the "graceful model upgrade" property — governance changes
// EmbeddingModelVersion from v1 to v2; all v1 in-memory vectors become misses
// automatically because sc.modelVersion changes but store.result.ModelVersion
// remains "v1".
func TestUpdateCacheParams_ModelVersionChange(t *testing.T) {
	sc, store := newTestCache("v1", 10)

	// Seed the cache with a v1 result
	store.hit = true
	store.result = CachedResult{
		ModelVersion:    "v1",
		ValidUntilEpoch: 100,
	}

	// Governance upgrades to v2
	sc.UpdateCacheParams(9700, "v2", 10)

	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 1)
	if hit {
		t.Fatal("expected all v1 cached results to become misses after governance upgrade to v2, got hit")
	}
}

// ── Feature-disabled fast path ────────────────────────────────────────────────

// TestDisabled_AlwaysMiss verifies that a disabled cache never hits.
func TestDisabled_AlwaysMiss(t *testing.T) {
	sc, store := newTestCache("v1", 10)
	store.hit = true
	store.result = CachedResult{ModelVersion: "v1", ValidUntilEpoch: 100}

	sc.SetEnabled(false)
	_, _, hit := sc.Lookup(context.Background(), []byte("prompt"), 1)
	if hit {
		t.Fatal("expected disabled cache to always return miss")
	}
}

// TestStats_AccurateCounting verifies hit/miss counters are accurate.
func TestStats_AccurateCounting(t *testing.T) {
	sc, store := newTestCache("v1", 10)

	// Miss: store has no hit
	store.hit = false
	_, _, _ = sc.Lookup(context.Background(), []byte("prompt"), 1) //nolint
	if h, m := sc.Stats(); h != 0 || m != 1 {
		t.Errorf("after miss: expected (0,1), got (%d,%d)", h, m)
	}

	// Hit: store returns valid result
	store.hit = true
	store.result = CachedResult{ModelVersion: "v1", ValidUntilEpoch: 100}
	_, _, _ = sc.Lookup(context.Background(), []byte("prompt"), 1) //nolint
	if h, m := sc.Stats(); h != 1 || m != 1 {
		t.Errorf("after hit: expected (1,1), got (%d,%d)", h, m)
	}
}

// ── InMemoryCacheStore validation matrix ──────────────────────────────────────
// These tests prove each cell of the validation matrix described in justrule.md.

// TestMatrix_L1_ExactMatch — PromptHash exact match returns 10000 bps (100%).
// Projected: user sends "What is 2+2?" twice. Second call → L1 HIT, no GPU
// round-trip, cached payload served immediately. SimilarityBps = 10000.
// MsgStartInference is still sent and MsgFinishInference closes the cycle.
func TestMatrix_L1_ExactMatch(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{
		PromptHash:      "abc123",
		ModelVersion:    "v1",
		ValidUntilEpoch: 999,
		OriginalEpoch:   1,
	}
	vec := []float32{1.0, 0.0, 0.0, 0.0}
	_ = store.Store(context.Background(), vec, result)

	r, hit := store.LookupExact("abc123")
	if !hit {
		t.Fatal("L1 exact match: expected HIT, got MISS")
	}
	if r.SimilarityBps != 10000 {
		t.Errorf("L1 exact match: expected SimilarityBps=10000, got %d", r.SimilarityBps)
	}
}

// TestMatrix_L1_WrongHash — different PromptHash → L1 MISS.
// Projected: "What is 2+2?" vs "What is 2+3?" — different canonical JSON,
// different PromptHash → falls through to L2 semantic search.
func TestMatrix_L1_WrongHash(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{PromptHash: "abc123", ModelVersion: "v1", ValidUntilEpoch: 999}
	_ = store.Store(context.Background(), []float32{1, 0, 0, 0}, result)

	_, hit := store.LookupExact("differenthash")
	if hit {
		t.Fatal("L1 wrong hash: expected MISS, got HIT")
	}
}

// TestMatrix_L2_SemanticHit — semantically similar vector above threshold → HIT.
// Projected: "What is 2+2?" and "Can you compute 2+2?" produce cosine ~0.97.
// Threshold 9700 bps (97%) → HIT, SimilarityBps reported as ~9700+.
func TestMatrix_L2_SemanticHit(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{PromptHash: "abc123", ModelVersion: "v1", ValidUntilEpoch: 999}
	vec := []float32{1.0, 0.0, 0.0, 0.0}
	_ = store.Store(context.Background(), vec, result)

	// 0.99/0.0 cosine with {1,0,0,0} → ~0.9999
	similar := []float32{0.99, 0.01, 0, 0}
	r, hit := store.Lookup(context.Background(), similar, 9700)
	if !hit {
		t.Fatal("L2 semantic: expected HIT for cosine~0.9999, threshold=9700")
	}
	if r.SimilarityBps < 9700 {
		t.Errorf("L2 semantic: expected SimilarityBps>=9700, got %d", r.SimilarityBps)
	}
}

// TestMatrix_L2_BelowThreshold — orthogonal vector → MISS regardless of L1.
// Projected: "What is 2+2?" vs "Tell me about black holes" — cosine ~0.01.
// Threshold 9700 bps → MISS → full GPU inference path taken.
func TestMatrix_L2_BelowThreshold(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{PromptHash: "abc123", ModelVersion: "v1", ValidUntilEpoch: 999}
	_ = store.Store(context.Background(), []float32{1, 0, 0, 0}, result)

	orthogonal := []float32{0, 1, 0, 0}
	_, hit := store.Lookup(context.Background(), orthogonal, 9700)
	if hit {
		t.Fatal("L2: expected MISS for orthogonal vector, got HIT")
	}
}

// TestMatrix_TTL_Eviction — entries with ValidUntilEpoch < current are evicted
// from both L1 and L2. Projected: result stored at epoch 1 with maxAge=10;
// at epoch 12 EvictExpired purges it. Next L1/L2 lookup → MISS.
func TestMatrix_TTL_Eviction(t *testing.T) {
	store := NewInMemoryCacheStore(4)
	result := CachedResult{PromptHash: "timed", ModelVersion: "v1", ValidUntilEpoch: 10}
	_ = store.Store(context.Background(), []float32{1, 0, 0, 0}, result)

	// Before eviction: L1 hit
	if _, hit := store.LookupExact("timed"); !hit {
		t.Fatal("before eviction: expected L1 HIT")
	}

	// Evict at epoch 11 (> ValidUntilEpoch 10)
	_ = store.EvictExpired(context.Background(), 11)

	if _, hit := store.LookupExact("timed"); hit {
		t.Fatal("after eviction: expected L1 MISS, got HIT")
	}
	if _, hit := store.Lookup(context.Background(), []float32{1, 0, 0, 0}, 9700); hit {
		t.Fatal("after eviction: expected L2 MISS, got HIT")
	}
}

// TestMatrix_ModelVersion_Invalidation — governance upgrade of EmbeddingModelVersion
// makes all old vectors misses without any manual eviction.
// Projected: governance changes v1→v2; all in-memory v1 results become MISS.
func TestMatrix_ModelVersion_Invalidation(t *testing.T) {
	sc, store := newTestCache("v1", 10)
	store.hit = true
	store.result = CachedResult{ModelVersion: "v1", ValidUntilEpoch: 100}

	// Before upgrade: HIT
	if _, _, hit := sc.Lookup(context.Background(), []byte("p"), 1); !hit {
		t.Fatal("before upgrade: expected HIT")
	}

	// Governance upgrade
	sc.UpdateCacheParams(9700, "v2", 10)
	if _, _, hit := sc.Lookup(context.Background(), []byte("p"), 1); hit {
		t.Fatal("after model upgrade: expected MISS for v1 result, got HIT")
	}
}
