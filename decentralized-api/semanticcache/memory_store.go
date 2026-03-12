package semanticcache

import (
	"context"
	"math"
	"sync"
)

// InMemoryCacheStore is the default CacheStore implementation.
//
// Two lookup levels:
//
//	Level 1 — PromptHash exact match (O(1), 100% same result guaranteed).
//	           sha256(canonical_JSON) matches X-Prompt-Hash protocol header.
//	           Same hash = same request = same BLS-verified result. No probabilistic
//	           uncertainty. Checked first; if hit, embedding is never computed.
//
//	Level 2 — Semantic cosine search (O(n), for semantically equivalent prompts).
//	           Uses all-MiniLM-L6-v2 vectors from ML-node embed endpoint.
//	           Only reached on Level 1 miss.
//
// Storage is in-process: zero external dependencies, works on every gonka node.
// Entries are lost on restart — acceptable for a cache (rebuilds naturally from
// new inferences).  EvictExpired removes entries with ValidUntilEpoch < current.
type InMemoryCacheStore struct {
	dims int

	mu sync.RWMutex

	// Level 1: exact PromptHash → result (O(1))
	exactIndex map[string]CachedResult

	// Level 2: semantic vector entries
	entries []vectorEntry
}

type vectorEntry struct {
	embedding []float32
	result    CachedResult
}

// NewInMemoryCacheStore creates a store for vectors of the given dimension count.
// dims must match the embedding model output (384 for all-MiniLM-L6-v2).
func NewInMemoryCacheStore(dims int) *InMemoryCacheStore {
	if dims <= 0 {
		dims = 384
	}
	return &InMemoryCacheStore{
		dims:       dims,
		exactIndex: make(map[string]CachedResult),
	}
}

// Lookup performs a Level 2 cosine similarity search.
// Level 1 (exact PromptHash) is handled separately by LookupExact; this method
// is called only when L1 already missed.
func (s *InMemoryCacheStore) Lookup(ctx context.Context, embedding []float32, thresholdBps uint32) (CachedResult, bool) {
	threshold := float64(thresholdBps) / 10000.0

	s.mu.RLock()
	defer s.mu.RUnlock()

	var best CachedResult
	bestSim := -1.0

	for _, e := range s.entries {
		sim := cosineSimilarity(embedding, e.embedding)
		if sim >= threshold && sim > bestSim {
			bestSim = sim
			best = e.result
		}
	}

	if bestSim < threshold {
		return CachedResult{}, false
	}
	best.SimilarityBps = uint32(bestSim * 10000)
	return best, true
}

// LookupExact returns a cached result by exact PromptHash (Level 1).
// Returns (result, true) if found; SimilarityBps is set to 10000 (exact match).
func (s *InMemoryCacheStore) LookupExact(promptHash string) (CachedResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.exactIndex[promptHash]
	if ok {
		r.SimilarityBps = 10000 // exact match
	}
	return r, ok
}

// Store saves the result at both Level 1 (PromptHash) and Level 2 (embedding vector).
func (s *InMemoryCacheStore) Store(ctx context.Context, embedding []float32, result CachedResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Level 1
	if result.PromptHash != "" {
		s.exactIndex[result.PromptHash] = result
	}

	// Level 2
	vec := make([]float32, len(embedding))
	copy(vec, embedding)
	s.entries = append(s.entries, vectorEntry{embedding: vec, result: result})
	return nil
}

// RecordReuse is a no-op for in-memory store.
// Reuse tracking is handled by QualityReporter which submits on-chain summaries.
func (s *InMemoryCacheStore) RecordReuse(_ context.Context, _ string, _ uint64, _ uint32) error {
	return nil
}

// EvictExpired removes all entries whose ValidUntilEpoch < currentEpoch.
func (s *InMemoryCacheStore) EvictExpired(_ context.Context, currentEpoch uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict from Level 2
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.result.ValidUntilEpoch >= currentEpoch {
			kept = append(kept, e)
		}
	}
	s.entries = kept

	// Evict from Level 1
	for k, r := range s.exactIndex {
		if r.ValidUntilEpoch < currentEpoch {
			delete(s.exactIndex, k)
		}
	}
	return nil
}

// cosineSimilarity returns the cosine similarity in [-1, 1] between two vectors.
// Returns 0 if either vector has zero norm.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
