package poc

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"common/logging"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
)

const distributionRetryInterval = 30 * time.Second

type commitState struct {
	count    uint32
	rootHash []byte
}

type commitKey struct {
	stage   int64
	modelID string
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
	lastCommitted           map[commitKey]commitState
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
		lastCommitted:      make(map[commitKey]commitState),
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

	w.mu.Lock()
	defer w.mu.Unlock()

	pocHeight := GetCurrentPocStageHeight(epochState)

	if pocHeight > 0 && w.currentPocHeight != pocHeight {
		w.currentPocHeight = pocHeight
		w.lastDistributionAttempt = time.Time{}
		w.lastCommitted = make(map[commitKey]commitState)
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
		if shouldRetry && w.hasPendingWeightDistribution(pocHeight) {
			w.submitWeightDistribution(pocHeight)
		}
	}
}

func (w *CommitWorker) maybeSubmitCommit(pocHeight int64) {
	stageStores, err := w.store.GetStoresForStage(pocHeight)
	if err != nil {
		logging.Debug("CommitWorker: no stores for height", types.PoC, "pocHeight", pocHeight, "error", err)
		return
	}
	if len(stageStores) == 0 {
		logging.Debug("CommitWorker: no stores for height", types.PoC, "pocHeight", pocHeight)
		return
	}

	entries := make([]*types.PoCV2CommitEntry, 0, len(stageStores))
	committedStates := make(map[commitKey]commitState, len(stageStores))
	for _, stageStore := range stageStores {
		if stageStore.Store == nil {
			continue
		}
		count, rootHash := stageStore.Store.GetFlushedRoot()
		if count == 0 || rootHash == nil {
			continue
		}

		key := commitKey{stage: pocHeight, modelID: stageStore.ModelID}
		last, hasLast := w.lastCommitted[key]

		if !hasLast && w.participantAddress != "" {
			queryClient := w.recorder.NewInferenceQueryClient()
			resp, err := queryClient.PoCV2StoreCommit(context.Background(), &types.QueryPoCV2StoreCommitRequest{
				PocStageStartBlockHeight: pocHeight,
				ParticipantAddress:       w.participantAddress,
				ModelId:                  stageStore.ModelID,
			})
			if err == nil && resp.Found {
				last = commitState{count: resp.Count}
				w.lastCommitted[key] = last
				hasLast = true
			}
		}

		if hasLast {
			if last.count == count && (last.rootHash == nil || bytes.Equal(last.rootHash, rootHash)) {
				continue
			}
			if count <= last.count {
				continue
			}
		}

		entries = append(entries, &types.PoCV2CommitEntry{
			ModelId:  stageStore.ModelID,
			Count:    count,
			RootHash: rootHash,
		})
		committedStates[key] = commitState{
			count:    count,
			rootHash: bytes.Clone(rootHash),
		}
	}
	if len(entries) == 0 {
		return
	}

	msg := &types.MsgPoCV2StoreCommit{
		PocStageStartBlockHeight: pocHeight,
		Entries:                  entries,
	}

	if err := w.recorder.SubmitPoCV2StoreCommit(msg); err != nil {
		logging.Warn("CommitWorker: commit failed", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}

	for key, state := range committedStates {
		w.lastCommitted[key] = state
	}
	logging.Debug("CommitWorker: committed", types.PoC,
		"pocHeight", pocHeight, "models", len(entries))
}

func (w *CommitWorker) submitWeightDistribution(pocHeight int64) {
	if w.participantAddress == "" {
		logging.Debug("CommitWorker: no participant address", types.PoC)
		return
	}

	stageStores, err := w.store.GetStoresForStage(pocHeight)
	if err != nil {
		logging.Debug("CommitWorker: no stores for distribution", types.PoC, "pocHeight", pocHeight, "error", err)
		return
	}
	if len(stageStores) == 0 {
		logging.Debug("CommitWorker: no stores for distribution", types.PoC, "pocHeight", pocHeight)
		return
	}
	defer func() {
		w.lastDistributionAttempt = time.Now()
	}()

	queryClient := w.recorder.NewInferenceQueryClient()
	entries := make([]*types.MLNodeDistributionEntry, 0, len(stageStores))
	totalNodes := 0
	for _, stageStore := range stageStores {
		if stageStore.Store == nil {
			continue
		}
		if err := stageStore.Store.Flush(); err != nil {
			logging.Warn("CommitWorker: flush failed", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "error", err)
		}

		commitResp, err := queryClient.PoCV2StoreCommit(context.Background(), &types.QueryPoCV2StoreCommitRequest{
			PocStageStartBlockHeight: pocHeight,
			ParticipantAddress:       w.participantAddress,
			ModelId:                  stageStore.ModelID,
		})
		if err != nil {
			logging.Warn("CommitWorker: failed to query last commit", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "error", err)
			continue
		}
		if !commitResp.Found || commitResp.Count == 0 {
			continue
		}

		if err := stageStore.Store.PrebuildSnapshot(commitResp.Count); err != nil {
			logging.Warn("CommitWorker: prebuild failed", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "count", commitResp.Count, "error", err)
		}

		distributionResp, err := queryClient.MLNodeWeightDistribution(context.Background(), &types.QueryMLNodeWeightDistributionRequest{
			PocStageStartBlockHeight: pocHeight,
			ParticipantAddress:       w.participantAddress,
			ModelId:                  stageStore.ModelID,
		})
		if err == nil && distributionResp.Found {
			continue
		}

		distribution, exact, err := stageStore.Store.GetNodeDistributionAt(commitResp.Count)
		if err != nil {
			logging.Error("CommitWorker: failed to get distribution", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "count", commitResp.Count, "error", err)
			continue
		}
		if !exact {
			logging.Warn("CommitWorker: using simulated distribution (history miss)", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "count", commitResp.Count)
		}
		if len(distribution) == 0 {
			continue
		}

		weights, err := getWeightDistribution(distribution, commitResp.Count)
		if err != nil {
			logging.Error("CommitWorker: failed to build weight distribution", types.PoC,
				"pocHeight", pocHeight, "modelId", stageStore.ModelID, "error", err)
			continue
		}

		entries = append(entries, &types.MLNodeDistributionEntry{
			ModelId: stageStore.ModelID,
			Weights: weights,
		})
		totalNodes += len(weights)
	}
	if len(entries) == 0 {
		logging.Debug("CommitWorker: all model distributions already on chain", types.PoC, "pocHeight", pocHeight)
		return
	}

	msg := &types.MsgMLNodeWeightDistribution{
		PocStageStartBlockHeight: pocHeight,
		Entries:                  entries,
	}

	if err := w.recorder.SubmitMLNodeWeightDistribution(msg); err != nil {
		logging.Warn("CommitWorker: distribution failed", types.PoC,
			"pocHeight", pocHeight, "error", err)
		return
	}

	logging.Info("CommitWorker: distributed weights", types.PoC,
		"pocHeight", pocHeight, "models", len(entries), "nodes", totalNodes)
}

func (w *CommitWorker) hasPendingWeightDistribution(pocHeight int64) bool {
	if w.participantAddress == "" {
		return false
	}

	stageStores, err := w.store.GetStoresForStage(pocHeight)
	if err != nil || len(stageStores) == 0 {
		return false
	}

	queryClient := w.recorder.NewInferenceQueryClient()
	for _, stageStore := range stageStores {
		if stageStore.Store == nil {
			continue
		}

		commitResp, err := queryClient.PoCV2StoreCommit(context.Background(), &types.QueryPoCV2StoreCommitRequest{
			PocStageStartBlockHeight: pocHeight,
			ParticipantAddress:       w.participantAddress,
			ModelId:                  stageStore.ModelID,
		})
		if err != nil {
			return true
		}
		if !commitResp.Found || commitResp.Count == 0 {
			continue
		}

		distributionResp, err := queryClient.MLNodeWeightDistribution(context.Background(), &types.QueryMLNodeWeightDistributionRequest{
			PocStageStartBlockHeight: pocHeight,
			ParticipantAddress:       w.participantAddress,
			ModelId:                  stageStore.ModelID,
		})
		if err != nil || !distributionResp.Found {
			return true
		}
	}

	return false
}

func getWeightDistribution(distribution map[string]uint32, targetCount uint32) ([]*types.MLNodeWeight, error) {
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
		weights := make([]*types.MLNodeWeight, 0, len(distribution))
		for nodeId, count := range distribution {
			weights = append(weights, &types.MLNodeWeight{
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

	weights := make([]*types.MLNodeWeight, 0, len(distribution))
	var scaledSum uint32
	for _, nodeId := range keys {
		count := distribution[nodeId]
		scaled := uint32(float64(count) * ratio)
		weights = append(weights, &types.MLNodeWeight{
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

func formatWeightDistribution(weights []*types.MLNodeWeight) string {
	if len(weights) == 0 {
		return "{}"
	}
	parts := make([]string, len(weights))
	for i, w := range weights {
		parts[i] = fmt.Sprintf("%s:%d", w.NodeId, w.Weight)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
