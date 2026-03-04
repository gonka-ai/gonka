package semanticcache

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
)

// CachedResult is a tamper-verifiable inference result that can be served
// directly to users without triggering a new GPU computation.
//
// Trust model: the ResponseHash (SHA-256 of ResponsePayload) is the same
// value that was committed on-chain via MsgFinishInference.ResponseHash.
// A cache consumer verifies sha256(ResponsePayload) == ResponseHash before
// accepting the result — this is the actual integrity mechanism used
// everywhere in this protocol (BLS is used for chain consensus, not for
// individual inference responses).
type CachedResult struct {
	// PromptHash is sha256(canonical(prompt)) — matches X-Prompt-Hash header.
	PromptHash string
	// ResponsePayload is the raw inference response bytes.
	ResponsePayload []byte
	// ResponseHash is sha256(ResponsePayload), identical to the value committed
	// on-chain in MsgFinishInference.ResponseHash.  Verified on every cache hit.
	ResponseHash string
	// InferenceId is the on-chain inference identifier used to query the
	// BLS threshold signing status from the chain.
	// Set at store time; used on cache HIT to fetch ThresholdSigningRequest.
	InferenceId string
	// BLSSignature is reserved for the quorum aggregate signature once the chain
	// wires RequestThresholdSignature to MsgFinishInference (future upgrade).
	// Currently empty; integrity is verified via ResponseHash (sha256).
	BLSSignature []byte
	// OriginalEpoch is the epoch index when the result was first computed.
	OriginalEpoch uint64
	// OriginalParticipantAddress is the bech32 address of the inference node
	// that produced this result — used for tracking CacheReuseEvents.
	OriginalParticipantAddress string
	// SimilarityBps is cosine similarity × 10 000 of the matched embedding.
	// 10 000 means exact hash match; values ≥ SimilarityThresholdBps are served.
	SimilarityBps uint32
	// ModelVersion is the embedding model identifier used when the source
	// inference was cached.  Must match SemanticCache.modelVersion (which
	// reflects the current CacheQualityParams.EmbeddingModelVersion governance
	// value) for the result to be served.  A mismatch means the similarity
	// score was computed with a different model — the cached vector is invalid.
	ModelVersion string
	// ValidUntilEpoch is the last epoch (inclusive) for which this result may
	// be served.  Computed as OriginalEpoch + MaxCacheAgeEpochs at store time.
	// After this epoch the result is treated as a miss.  EvictExpired purges
	// expired entries from both L1 and L2 at each epoch boundary.
	ValidUntilEpoch uint64
}

// CacheStore is the vector-database backend for semantic cache lookups.
//
// The default implementation is InMemoryCacheStore — zero external dependencies,
// works on every gonka node. The interface allows swapping to any ANN backend.
type CacheStore interface {
	// Lookup searches for semantically equivalent cached results.
	// Returns (result, true) when a result with similarity ≥ thresholdBps is found.
	Lookup(ctx context.Context, embedding []float32, thresholdBps uint32) (CachedResult, bool)

	// Store persists a new inference result with its embedding vector.
	Store(ctx context.Context, embedding []float32, result CachedResult) error

	// RecordReuse increments the reuse counter for the given participant in the
	// current epoch.  Called on every cache hit.
	RecordReuse(ctx context.Context, participantAddress string, epochIndex uint64, similarityBps uint32) error

	// EvictExpired removes all points whose ValidUntilEpoch < currentEpoch.
	// Must be called periodically (e.g., from a background goroutine) to keep
	// the vector collection bounded.
	EvictExpired(ctx context.Context, currentEpoch uint64) error
}

// SemanticCache is the main entry point for the semantic caching layer.
// It wraps an Embedder and a CacheStore to provide cache-aware routing.
type SemanticCache struct {
	embedder Embedder
	store    CacheStore

	mu                sync.RWMutex // protects thresholdBps, modelVersion, maxCacheAgeEpochs
	thresholdBps      uint32       // governance: SimilarityThresholdBps
	modelVersion      string       // governance: EmbeddingModelVersion — must match CachedResult.ModelVersion
	maxCacheAgeEpochs uint64       // governance: MaxCacheAgeEpochs — off-chain TTL for cached results

	enabled   uint32 // atomic bool: 1 = enabled
	hitCount  int64  // prometheus-ready counter
	missCount int64  // prometheus-ready counter
}

// NewSemanticCache constructs a SemanticCache.
// Pass enabled=false (or use a StubEmbedder + NopStore) when the feature is
// governed-off; all calls to Lookup will return (_, false).
//
// modelVersion must match CacheQualityParams.EmbeddingModelVersion from chain params.
// maxCacheAgeEpochs must match CacheQualityParams.MaxCacheAgeEpochs from chain params.
func NewSemanticCache(
	embedder Embedder,
	store CacheStore,
	thresholdBps uint32,
	modelVersion string,
	maxCacheAgeEpochs uint64,
	enabled bool,
) *SemanticCache {
	var e uint32
	if enabled {
		e = 1
	}
	return &SemanticCache{
		embedder:          embedder,
		store:             store,
		thresholdBps:      thresholdBps,
		modelVersion:      modelVersion,
		maxCacheAgeEpochs: maxCacheAgeEpochs,
		enabled:           e,
	}
}

// SetEnabled atomically toggles the feature.  Called when governance params change.
func (sc *SemanticCache) SetEnabled(enabled bool) {
	if enabled {
		atomic.StoreUint32(&sc.enabled, 1)
	} else {
		atomic.StoreUint32(&sc.enabled, 0)
	}
}

// UpdateCacheParams refreshes the governance-managed parameters at runtime.
// Called when an on-chain governance proposal updates CacheQualityParams.
//
// Changing modelVersion immediately causes all previously cached results with a
// different ModelVersion to be served as misses — no manual flush needed.
// Old entries are naturally evicted when they fail the model version check.
func (sc *SemanticCache) UpdateCacheParams(thresholdBps uint32, modelVersion string, maxCacheAgeEpochs uint64) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.thresholdBps = thresholdBps
	sc.modelVersion = modelVersion
	sc.maxCacheAgeEpochs = maxCacheAgeEpochs
}

// Lookup embeds the prompt text and searches for a semantically equivalent
// cached result.  Returns (result, embedding, true) on a cache hit.
//
// The returned embedding is the vector computed for this prompt.  Pass it to
// StoreResult to avoid re-computing the embedding on the store path — one
// ML-node call per request instead of two.
//
// promptText must be the semantic content of the user messages only, NOT the
// full canonical JSON request body.  This ensures semantically identical
// prompts with different temperature/seed/model parameters hit the cache.
// The canonical JSON is used separately for PromptHash and on-chain integrity.
//
// A result is served only when ALL three conditions hold:
//  1. Similarity ≥ SimilarityThresholdBps (semantic equivalence)
//  2. result.ModelVersion == sc.modelVersion (embedding model consistency)
//  3. result.ValidUntilEpoch >= currentEpoch (TTL not expired)
//
// Performance: Embed ~2ms (ML-node all-MiniLM-L6-v2) + cosine O(n) in-memory.
func (sc *SemanticCache) Lookup(ctx context.Context, promptText []byte, currentEpoch uint64) (CachedResult, []float32, bool) {
	if atomic.LoadUint32(&sc.enabled) == 0 {
		return CachedResult{}, nil, false
	}

	sc.mu.RLock()
	thresholdBps := sc.thresholdBps
	modelVersion := sc.modelVersion
	sc.mu.RUnlock()

	embedding, err := sc.embedder.Embed(ctx, promptText)
	if err != nil {
		// Embedding failure is non-fatal: fall through to normal inference path.
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, nil, false
	}

	result, hit := sc.store.Lookup(ctx, embedding, thresholdBps)
	if !hit {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, embedding, false
	}

	// Model version check: results from a different embedding model produce
	// incomparable similarity scores and must not be served.
	if result.ModelVersion != modelVersion {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, embedding, false
	}

	// TTL check: results older than MaxCacheAgeEpochs are stale.
	if result.ValidUntilEpoch < currentEpoch {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, embedding, false
	}

	atomic.AddInt64(&sc.hitCount, 1)
	return result, embedding, true
}

// StoreResult persists the inference result using a pre-computed embedding.
//
// embedding must be the vector returned by the Lookup call that preceded this
// store (on a cache miss).  Passing the pre-computed embedding eliminates a
// second ML-node round-trip — the embedder is called exactly once per request.
//
// If embedding is nil (e.g., Lookup was skipped), the embedder is called here.
func (sc *SemanticCache) StoreResult(ctx context.Context, promptText []byte, embedding []float32, result CachedResult, currentEpoch uint64) error {
	if atomic.LoadUint32(&sc.enabled) == 0 {
		return nil
	}

	sc.mu.RLock()
	result.ModelVersion = sc.modelVersion
	result.ValidUntilEpoch = currentEpoch + sc.maxCacheAgeEpochs
	sc.mu.RUnlock()
	result.OriginalEpoch = currentEpoch

	if len(embedding) == 0 {
		var err error
		embedding, err = sc.embedder.Embed(ctx, promptText)
		if err != nil {
			return err
		}
	}
	return sc.store.Store(ctx, embedding, result)
}

// RecordReuse delegates to the underlying CacheStore.RecordReuse.
// Called after a successful cache hit to update the quality-tracking counters
// that feed into MsgSubmitCacheQualitySummary on-chain submissions.
func (sc *SemanticCache) RecordReuse(ctx context.Context, participantAddress string, epochIndex uint64, similarityBps uint32) error {
	return sc.store.RecordReuse(ctx, participantAddress, epochIndex, similarityBps)
}

// EvictExpired delegates TTL cleanup to the underlying store.
// Intended to be called from a background goroutine once per epoch.
func (sc *SemanticCache) EvictExpired(ctx context.Context, currentEpoch uint64) error {
	if atomic.LoadUint32(&sc.enabled) == 0 {
		return nil
	}
	return sc.store.EvictExpired(ctx, currentEpoch)
}

// LookupByPromptHash performs Level 1 exact-match cache lookup by PromptHash.
//
// PromptHash = sha256(canonical_JSON of the inference request), identical to the
// value in the X-Prompt-Hash protocol header and MsgFinishInference.PromptHash.
// Same hash guarantees same request → same on-chain verified result.
// No embedding computation, no probabilistic similarity — pure O(1) map lookup.
//
// Called inside handleExecutorRequest alongside L2 cosine lookup.
// On L1 HIT: serve cached payload + call sendInferenceTransaction to close
// the on-chain cycle — the node earns CacheQualityWeight without GPU work.
func (sc *SemanticCache) LookupByPromptHash(promptHash string, currentEpoch uint64) (CachedResult, bool) {
	if atomic.LoadUint32(&sc.enabled) == 0 {
		return CachedResult{}, false
	}

	type exactLookup interface {
		LookupExact(promptHash string) (CachedResult, bool)
	}
	el, ok := sc.store.(exactLookup)
	if !ok {
		return CachedResult{}, false
	}

	sc.mu.RLock()
	modelVersion := sc.modelVersion
	sc.mu.RUnlock()

	result, hit := el.LookupExact(promptHash)
	if !hit {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, false
	}
	if result.ModelVersion != modelVersion {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, false
	}
	if result.ValidUntilEpoch < currentEpoch {
		atomic.AddInt64(&sc.missCount, 1)
		return CachedResult{}, false
	}
	atomic.AddInt64(&sc.hitCount, 1)
	return result, true
}

// ModelVersion returns the current governance-managed embedding model version.
// Used by QualityReporter to populate EmbeddingModelVersion in the on-chain summary.
func (sc *SemanticCache) ModelVersion() string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.modelVersion
}

// Stats returns cumulative hit and miss counts since last restart.
func (sc *SemanticCache) Stats() (hits, misses int64) {
	return atomic.LoadInt64(&sc.hitCount), atomic.LoadInt64(&sc.missCount)
}

// HitRate returns the cache hit rate as a float in [0, 1].
func (sc *SemanticCache) HitRate() float64 {
	h := atomic.LoadInt64(&sc.hitCount)
	m := atomic.LoadInt64(&sc.missCount)
	total := h + m
	if total == 0 {
		return 0
	}
	return math.Round(float64(h)/float64(total)*10000) / 10000
}

// ── NopStore ──────────────────────────────────────────────────────────────────
// NopStore is a no-op CacheStore used when semantic caching is disabled or
// in unit tests that do not require a real vector database.

type NopStore struct{}

func (n NopStore) Lookup(_ context.Context, _ []float32, _ uint32) (CachedResult, bool) {
	return CachedResult{}, false
}
func (n NopStore) Store(_ context.Context, _ []float32, _ CachedResult) error { return nil }
func (n NopStore) RecordReuse(_ context.Context, _ string, _ uint64, _ uint32) error {
	return nil
}
func (n NopStore) EvictExpired(_ context.Context, _ uint64) error { return nil }
