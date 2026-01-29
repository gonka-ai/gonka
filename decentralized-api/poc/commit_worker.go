package poc

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

const distributionRetryInterval = 30 * time.Second

type commitState struct {
	count    uint32
	rootHash []byte
}

type CommitWorker struct {
	store              *artifacts.ManagedArtifactStore
	recorder           cosmosclient.CosmosMessageClient
	tracker            *chainphase.ChainPhaseTracker
	participantAddress string

	interval time.Duration
	stop     chan struct{}
	done     chan struct{}

	mu                      sync.Mutex
	currentPocHeight        int64
	lastDistributionAttempt time.Time
	lastCommitted           map[int64]commitState
}

// NewCommitWorker creates and starts a new commit worker.
// The worker runs until Close() is called.
func NewCommitWorker(
	store *artifacts.ManagedArtifactStore,
	recorder cosmosclient.CosmosMessageClient,
	tracker *chainphase.ChainPhaseTracker,
	participantAddress string,
	interval time.Duration,
) *CommitWorker {
	w := &CommitWorker{
		store:              store,
		recorder:           recorder,
		tracker:            tracker,
		participantAddress: participantAddress,
		interval:           interval,
		stop:               make(chan struct{}),
		done:               make(chan struct{}),
		lastCommitted:      make(map[int64]commitState),
	}

	// Start flush - always on (same interval as commits)
	store.StartPeriodicFlush(interval)

	go w.run()
	logging.Info("CommitWorker started", types.PoC, "interval", interval)
	return w
}

// Close stops the worker and waits for it to finish.
func (w *CommitWorker) Close() {
	close(w.stop)
	<-w.done
	w.store.StopPeriodicFlush()
	logging.Info("CommitWorker stopped", types.PoC)
}

func (w *CommitWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.tick()
		case <-w.stop:
			return
		}
	}
}

func (w *CommitWorker) tick() {
	epochState := w.tracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		return
	}

	if !ShouldUseV2FromEpochState(epochState) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	pocHeight := GetCurrentPocStageHeight(epochState)

	if pocHeight > 0 && w.currentPocHeight != pocHeight {
		w.currentPocHeight = pocHeight
		w.lastDistributionAttempt = time.Time{}
		w.lastCommitted = make(map[int64]commitState)
	}

	if pocHeight > 0 {
		canCommit := ShouldAcceptStoreCommit(epochState, pocHeight)
		logging.Debug("CommitWorker: tick", types.PoC,
			"phase", epochState.CurrentPhase,
			"pocHeight", pocHeight,
			"canCommit", canCommit)
		if canCommit {
			w.maybeSubmitCommit(pocHeight)
		}
	}

	if ShouldHaveDistributedWeights(epochState) && pocHeight > 0 {
		shouldRetry := w.lastDistributionAttempt.IsZero() ||
			time.Since(w.lastDistributionAttempt) > distributionRetryInterval
		onChain := w.isDistributionOnChain(pocHeight)
		logging.Debug("CommitWorker: distribution check", types.PoC,
			"pocHeight", pocHeight,
			"shouldRetry", shouldRetry,
			"lastAttemptIsZero", w.lastDistributionAttempt.IsZero(),
			"onChain", onChain)
		if shouldRetry && !onChain {
			w.submitWeightDistribution(pocHeight)
		}
	}
}

func (w *CommitWorker) maybeSubmitCommit(pocHeight int64) {
	store, err := w.store.GetStore(pocHeight)
	if err != nil || store == nil {
		logging.Debug("CommitWorker: no store for height", types.PoC, "pocHeight", pocHeight)
		return
	}

	count, rootHash := store.GetFlushedRoot()
	if count == 0 || rootHash == nil {
		logging.Debug("CommitWorker: no flushed data", types.PoC, "pocHeight", pocHeight, "count", count)
		return
	}

	// Skip if unchanged since last commit
	last := w.lastCommitted[pocHeight]
	if last.count == count && bytes.Equal(last.rootHash, rootHash) {
		return
	}

	msg := &inference.MsgPoCV2StoreCommit{
		PocStageStartBlockHeight: pocHeight,
		Count:                    count,
		RootHash:                 rootHash,
	}

	if err := w.recorder.SubmitPoCV2StoreCommit(msg); err != nil {
		logging.Warn("CommitWorker: commit failed", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}

	w.lastCommitted[pocHeight] = commitState{count, rootHash}
	logging.Debug("CommitWorker: committed", types.PoC,
		"pocHeight", pocHeight, "count", count)
}

func (w *CommitWorker) isDistributionOnChain(pocHeight int64) bool {
	if w.participantAddress == "" {
		return false
	}
	queryClient := w.recorder.NewInferenceQueryClient()
	resp, err := queryClient.MLNodeWeightDistribution(context.Background(), &types.QueryMLNodeWeightDistributionRequest{
		PocStageStartBlockHeight: pocHeight,
		ParticipantAddress:       w.participantAddress,
	})
	return err == nil && resp.Found
}

func (w *CommitWorker) submitWeightDistribution(pocHeight int64) {
	store, err := w.store.GetStore(pocHeight)
	if err != nil || store == nil {
		logging.Debug("CommitWorker: no store", types.PoC, "pocHeight", pocHeight)
		return
	}

	if w.participantAddress == "" {
		logging.Debug("CommitWorker: no participant address", types.PoC)
		return
	}

	queryClient := w.recorder.NewInferenceQueryClient()
	resp, err := queryClient.PoCV2StoreCommit(context.Background(), &types.QueryPoCV2StoreCommitRequest{
		PocStageStartBlockHeight: pocHeight,
		ParticipantAddress:       w.participantAddress,
	})
	if err != nil {
		logging.Warn("CommitWorker: failed to query last commit", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}
	if !resp.Found || resp.Count == 0 {
		logging.Debug("CommitWorker: no committed snapshot", types.PoC,
			"pocHeight", pocHeight, "found", resp.Found, "count", resp.Count)
		return
	}

	if err := store.Flush(); err != nil {
		logging.Warn("CommitWorker: flush failed", types.PoC, "pocHeight", pocHeight, "error", err)
	}

	distribution := store.GetNodeDistribution()
	if len(distribution) == 0 {
		logging.Debug("CommitWorker: empty distribution", types.PoC, "pocHeight", pocHeight)
		return
	}

	weights, err := getWeightDistribution(distribution, resp.Count)
	if err != nil {
		logging.Error("CommitWorker: failed to build weight distribution", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}

	msg := &inference.MsgMLNodeWeightDistribution{
		PocStageStartBlockHeight: pocHeight,
		Weights:                  weights,
	}

	if err := w.recorder.SubmitMLNodeWeightDistribution(msg); err != nil {
		logging.Warn("CommitWorker: distribution failed", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}

	w.lastDistributionAttempt = time.Now()

	logging.Info("CommitWorker: distributed weights", types.PoC,
		"pocHeight", pocHeight, "nodes", len(weights), "count", resp.Count,
		"distribution", formatWeightDistribution(weights))
}

func getWeightDistribution(distribution map[string]uint32, targetCount uint32) ([]*inference.MLNodeWeight, error) {
	if len(distribution) == 0 {
		return nil, fmt.Errorf("empty distribution")
	}
	if targetCount == 0 {
		return nil, fmt.Errorf("targetCount is 0")
	}

	var localSum uint32
	for _, count := range distribution {
		localSum += count
	}

	if localSum == 0 {
		return nil, fmt.Errorf("distribution sum is 0")
	}

	if localSum == targetCount {
		weights := make([]*inference.MLNodeWeight, 0, len(distribution))
		for nodeId, count := range distribution {
			weights = append(weights, &inference.MLNodeWeight{
				NodeId: nodeId,
				Weight: count,
			})
		}
		return weights, nil
	}

	logging.Warn("CommitWorker: adjusting distribution proportionally", types.PoC,
		"localSum", localSum, "targetCount", targetCount)

	ratio := float64(targetCount) / float64(localSum)

	keys := make([]string, 0, len(distribution))
	for nodeId := range distribution {
		keys = append(keys, nodeId)
	}
	sort.Strings(keys)

	weights := make([]*inference.MLNodeWeight, 0, len(distribution))
	var scaledSum uint32
	for _, nodeId := range keys {
		count := distribution[nodeId]
		scaled := uint32(float64(count) * ratio)
		weights = append(weights, &inference.MLNodeWeight{
			NodeId: nodeId,
			Weight: scaled,
		})
		scaledSum += scaled
	}

	diff := int(targetCount) - int(scaledSum)
	for i := 0; diff > 0; i++ {
		weights[i%len(weights)].Weight++
		diff--
	}

	return weights, nil
}

func formatWeightDistribution(weights []*inference.MLNodeWeight) string {
	if len(weights) == 0 {
		return "{}"
	}
	parts := make([]string, len(weights))
	for i, w := range weights {
		parts[i] = fmt.Sprintf("%s:%d", w.NodeId, w.Weight)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
