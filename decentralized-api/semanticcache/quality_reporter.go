package semanticcache

import (
	"context"
	"sync"
	"sync/atomic"

	sdk "github.com/cosmos/cosmos-sdk/types"
	inftypes "github.com/productscience/inference/x/inference/types"
)

// TxSender is the subset of cosmosclient.InferenceCosmosClient needed by
// QualityReporter. Using an interface keeps the package dependency minimal
// and makes the reporter unit-testable without a full cosmos client.
type TxSender interface {
	GetAccountAddress() string
	SendTransactionAsyncNoRetry(msg sdk.Msg) (*sdk.TxResponse, error)
}

// epochStats accumulates per-epoch cache quality metrics in memory.
type epochStats struct {
	reuseCount    int64
	computeCount  int64
	similaritySum int64 // sum of SimilarityBps for average computation
}

// QualityReporter accumulates cache reuse events per epoch and submits a
// MsgSubmitCacheQualitySummary to the chain at each epoch boundary.
//
// This closes the economic loop described in CacheQualityParams:
// participants earn weight bonus proportional to their cache reuse count,
// capped at MaxWeightFractionBps of standard PoC weight.
type QualityReporter struct {
	sender       TxSender
	modelVersion func() string // returns current governance EmbeddingModelVersion

	mu           sync.Mutex
	currentEpoch uint64
	stats        epochStats

	submitted uint32 // atomic bool: 1 if already submitted for currentEpoch
}

// NewQualityReporter constructs a QualityReporter.
// modelVersionFn must return the current EmbeddingModelVersion from governance params.
func NewQualityReporter(sender TxSender, modelVersionFn func() string) *QualityReporter {
	return &QualityReporter{
		sender:       sender,
		modelVersion: modelVersionFn,
	}
}

// RecordReuse records one cache reuse event for the given epoch.
// Called on every cache HIT from the request handler goroutine (non-blocking).
func (r *QualityReporter) RecordReuse(epochIndex uint64, similarityBps uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if epochIndex != r.currentEpoch {
		r.currentEpoch = epochIndex
		r.stats = epochStats{}
		atomic.StoreUint32(&r.submitted, 0)
	}
	r.stats.reuseCount++
	r.stats.similaritySum += int64(similarityBps)
}

// RecordCompute records one GPU inference that was seeded into the cache.
// Called after a successful StoreResult.
func (r *QualityReporter) RecordCompute(epochIndex uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if epochIndex != r.currentEpoch {
		r.currentEpoch = epochIndex
		r.stats = epochStats{}
		atomic.StoreUint32(&r.submitted, 0)
	}
	r.stats.computeCount++
}

// SubmitEpoch submits MsgSubmitCacheQualitySummary for the given epoch.
// Called once per epoch boundary from the chainPhaseTracker epoch listener.
// Idempotent: does nothing if already submitted for this epoch or no data.
func (r *QualityReporter) SubmitEpoch(ctx context.Context, epochIndex uint64) error {
	if !atomic.CompareAndSwapUint32(&r.submitted, 0, 1) {
		return nil // already submitted for this epoch
	}

	r.mu.Lock()
	stats := r.stats
	r.mu.Unlock()

	if stats.reuseCount == 0 && stats.computeCount == 0 {
		return nil // nothing to report — no activity this epoch
	}

	var avgSimilarityBps uint32
	if stats.reuseCount > 0 {
		avgSimilarityBps = uint32(stats.similaritySum / stats.reuseCount)
	}

	msg := &inftypes.MsgSubmitCacheQualitySummary{
		Creator: r.sender.GetAccountAddress(),
		Summary: inftypes.CacheQualityEpochSummary{
			ParticipantAddress:    r.sender.GetAccountAddress(),
			EpochIndex:            epochIndex,
			CacheReuseCount:       stats.reuseCount,
			OriginalComputeCount:  stats.computeCount,
			AvgSimilarityBps:      avgSimilarityBps,
			EmbeddingModelVersion: r.modelVersion(),
		},
	}

	_, err := r.sender.SendTransactionAsyncNoRetry(msg)
	return err
}
