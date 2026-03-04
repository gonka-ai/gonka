// Package semanticcache implements semantic inference result caching for the
// Gonka decentralized-api layer.
//
// Architecture:
//
//	User Request
//	     │
//	     ├─ L1: PromptHash exact-match (InMemoryCacheStore, O(1), no embedding needed)
//	     │    HIT ──────────► [VerifyResponseHash] → serve cached response (<1ms)
//	     │    MISS
//	     ▼
//	[ComputeEmbedding]   ← ~2ms CPU via ML-node /api/v1/embed (all-MiniLM-L6-v2)
//	     │
//	     ├─ L2: cosine similarity search (InMemoryCacheStore, O(n))
//	     │    HIT ──────────► [VerifyResponseHash] → serve cached response (~3ms)
//	     │    MISS
//	     ▼
//	[InferenceNodes]     ← standard GPU path (seconds)
//	     │
//	     ▼
//	[StoreCachedResult]  ← store embedding + response + ResponseHash at both L1 and L2
//
// Trust model:
//   - Every stored result carries ResponseHash = sha256(ResponsePayload), identical
//     to the value committed on-chain via MsgFinishInference.ResponseHash.
//   - The handler verifies sha256(cached.ResponsePayload) == cached.ResponseHash
//     before serving — the same integrity check validators use for fresh inferences.
//   - BLS is used for chain consensus (group key validation), not for individual
//     inference responses at this protocol level.
//
// Embedder implementation:
//   - MLNodeEmbedder calls the ML-node management API (/api/v1/embed).
//   - The endpoint runs all-MiniLM-L6-v2 on CPU inside the ML-node process.
//   - It does NOT lock the inference GPU — the call uses the PoC/management port
//     which is always available, matching how other non-inference ML-node calls work.
//   - Dimensions: 384 (all-MiniLM-L6-v2 output; matches InMemoryCacheStore dims).
package semanticcache

import (
	"context"
	"fmt"

	"decentralized-api/mlnodeclient"
)

// Embedder converts an inference prompt payload into a fixed-length float32
// embedding vector used for nearest-neighbour search.
type Embedder interface {
	// Embed returns the embedding vector for the given prompt payload.
	// The payload is the canonical JSON of the inference request
	// (same bytes as used for ComputePromptHash on-chain).
	Embed(ctx context.Context, promptPayload []byte) ([]float32, error)

	// Dimensions returns the number of dimensions produced by this embedder.
	// Must be consistent across all calls on the same instance.
	Dimensions() int
}

// MLNodeEmbedder calls the ML-node /api/v1/embed endpoint to produce
// all-MiniLM-L6-v2 embeddings (384 dimensions, Cosine space).
//
// The ML-node runs the embedding model on CPU — independent of its
// inference/PoC GPU state.  Calls go to the management (PoC) port,
// not through the inference broker lock.
type MLNodeEmbedder struct {
	client mlnodeclient.MLNodeClient
	dims   int
}

// NewMLNodeEmbedder constructs an embedder backed by the given ML-node client.
// dims must match InMemoryCacheStore dims (384 for all-MiniLM-L6-v2).
func NewMLNodeEmbedder(client mlnodeclient.MLNodeClient, dims int) *MLNodeEmbedder {
	if dims <= 0 {
		dims = 384
	}
	return &MLNodeEmbedder{client: client, dims: dims}
}

func (e *MLNodeEmbedder) Embed(ctx context.Context, promptPayload []byte) ([]float32, error) {
	resp, err := e.client.Embed(ctx, string(promptPayload))
	if err != nil {
		return nil, fmt.Errorf("mlnode embed: %w", err)
	}
	if len(resp.Embedding) == 0 {
		return nil, fmt.Errorf("mlnode embed: empty embedding returned")
	}
	if len(resp.Embedding) != e.dims {
		return nil, fmt.Errorf("mlnode embed: expected %d dimensions, got %d", e.dims, len(resp.Embedding))
	}
	return resp.Embedding, nil
}

func (e *MLNodeEmbedder) Dimensions() int { return e.dims }

// StubEmbedder is a no-op embedder that returns deterministic zero vectors.
// Used when CacheQualityParams.Enabled == false; all lookups will miss.
type StubEmbedder struct {
	dims int
}

func NewStubEmbedder(dims int) *StubEmbedder {
	if dims <= 0 {
		dims = 384
	}
	return &StubEmbedder{dims: dims}
}

func (s *StubEmbedder) Embed(_ context.Context, _ []byte) ([]float32, error) {
	return make([]float32, s.dims), nil
}

func (s *StubEmbedder) Dimensions() int { return s.dims }
