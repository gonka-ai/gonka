package internal

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// BandwidthLimiter provides a simple mechanism to enforce bandwidth limits.
// Minimalistic approach: use cached epoch data, refresh only when epoch changes.
type BandwidthLimiter struct {
	mu                    sync.RWMutex
	limitsPerBlockKB      uint64
	usagePerBlock         map[int64]float64
	cleanupInterval       time.Duration
	requestLifespanBlocks int64

	// Configurable coefficients from chain parameters
	kbPerInputToken  float64
	kbPerOutputToken float64

	// Inference count limiting
	inferencesPerBlock    map[int64]int64
	maxInferencesPerBlock uint64
	defaultInferenceLimit uint64

	recorder              cosmosclient.CosmosMessageClient
	defaultLimit          uint64
	epochCache            *EpochGroupDataCache
	phaseTracker          ChainPhaseTracker
	configManager         ConfigManager
	cachedLimitEpochIndex uint64
	cachedWeightLimit     uint64
}

// CanAcceptRequest checks both bandwidth and inference count limits.
// Returns (canAccept, estimatedKB) - estimatedKB is used for recording.
func (bl *BandwidthLimiter) CanAcceptRequest(blockHeight int64, promptTokens, maxTokens int) (bool, float64) {
	bl.maybeUpdateLimits()

	bl.mu.RLock()
	defer bl.mu.RUnlock()

	windowSize := bl.requestLifespanBlocks + 1

	// Check bandwidth limit
	estimatedKB := float64(promptTokens)*bl.kbPerInputToken + float64(maxTokens)*bl.kbPerOutputToken
	totalUsage := 0.0
	for i := blockHeight; i <= blockHeight+bl.requestLifespanBlocks; i++ {
		totalUsage += bl.usagePerBlock[i]
	}
	avgUsage := totalUsage / float64(windowSize)
	estimatedKBPerBlock := estimatedKB / float64(windowSize)

	if avgUsage+estimatedKBPerBlock > float64(bl.limitsPerBlockKB) {
		logging.Info("Bandwidth limit exceeded", types.Config,
			"avgUsage", avgUsage, "estimatedKB", estimatedKBPerBlock, "limit", bl.limitsPerBlockKB)
		return false, estimatedKB
	}

	// Check inference count limit (if enabled)
	if bl.maxInferencesPerBlock > 0 {
		var totalInferences int64
		for i := blockHeight; i <= blockHeight+bl.requestLifespanBlocks; i++ {
			totalInferences += bl.inferencesPerBlock[i]
		}
		avgInferences := float64(totalInferences) / float64(windowSize)

		if avgInferences+1.0/float64(windowSize) > float64(bl.maxInferencesPerBlock) {
			logging.Info("Inference count limit exceeded", types.Config,
				"avgInferences", avgInferences, "limit", bl.maxInferencesPerBlock)
			return false, estimatedKB
		}
	}

	return true, estimatedKB
}

func (bl *BandwidthLimiter) maybeUpdateLimits() {
	if bl.phaseTracker == nil {
		return
	}

	epochState := bl.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		return
	}

	currentEpochIndex := epochState.LatestEpoch.EpochIndex
	bl.mu.RLock()
	cachedEpochIndex := bl.cachedLimitEpochIndex
	bl.mu.RUnlock()

	if bl.configManager != nil {
		updated := bl.updateParametersFromConfig()
		if updated {
			// Force limit recalculation for the current epoch when config changes.
			// This allows toggling inference limits (including to 0) without waiting for epoch transition.
			bl.mu.Lock()
			bl.cachedLimitEpochIndex = 0
			bl.mu.Unlock()
		}
	}

	if cachedEpochIndex == currentEpochIndex {
		return
	}
	bl.updateWeightBasedLimit(currentEpochIndex)
}

func (bl *BandwidthLimiter) updateParametersFromConfig() bool {
	validationParams := bl.configManager.GetValidationParams()
	bandwidthParams := bl.configManager.GetBandwidthParams()

	bl.mu.Lock()
	defer bl.mu.Unlock()

	updated := false

	if validationParams.ExpirationBlocks > 0 && bl.requestLifespanBlocks != validationParams.ExpirationBlocks {
		bl.requestLifespanBlocks = validationParams.ExpirationBlocks
		updated = true
	}

	if bandwidthParams.KbPerInputToken > 0 && bl.kbPerInputToken != bandwidthParams.KbPerInputToken {
		bl.kbPerInputToken = bandwidthParams.KbPerInputToken
		updated = true
	}

	if bandwidthParams.KbPerOutputToken > 0 && bl.kbPerOutputToken != bandwidthParams.KbPerOutputToken {
		bl.kbPerOutputToken = bandwidthParams.KbPerOutputToken
		updated = true
	}

	if bandwidthParams.EstimatedLimitsPerBlockKb > 0 && bl.defaultLimit != bandwidthParams.EstimatedLimitsPerBlockKb {
		bl.defaultLimit = bandwidthParams.EstimatedLimitsPerBlockKb
		updated = true
	}

	// 0 is meaningful: it disables inference count limiting.
	if bl.defaultInferenceLimit != bandwidthParams.MaxInferencesPerBlock {
		bl.defaultInferenceLimit = bandwidthParams.MaxInferencesPerBlock
		updated = true
	}

	if updated {
		logging.Info("Updated bandwidth parameters from config", types.Config,
			"lifespanBlocks", bl.requestLifespanBlocks,
			"kbPerInputToken", bl.kbPerInputToken,
			"kbPerOutputToken", bl.kbPerOutputToken,
			"defaultLimit", bl.defaultLimit,
			"defaultInferenceLimit", bl.defaultInferenceLimit)
	}
	return updated
}

func (bl *BandwidthLimiter) updateWeightBasedLimit(currentEpochIndex uint64) {
	if bl.epochCache == nil || bl.recorder == nil {
		logging.Warn("Epoch cache or recorder is nil, skipping weight-based limit update", types.Config)
		return
	}

	bl.mu.RLock()
	if bl.cachedLimitEpochIndex == currentEpochIndex && bl.cachedWeightLimit > 0 {
		bl.mu.RUnlock()
		return
	}
	bl.mu.RUnlock()

	newKBLimit, newInferenceLimit := bl.calculateUniformLimits(currentEpochIndex)

	bl.mu.Lock()
	defer bl.mu.Unlock()

	if bl.limitsPerBlockKB != newKBLimit {
		bl.limitsPerBlockKB = newKBLimit
		logging.Info("Updated bandwidth limit", types.Config,
			"newLimit", newKBLimit, "epoch", currentEpochIndex)
	}

	if bl.maxInferencesPerBlock != newInferenceLimit {
		bl.maxInferencesPerBlock = newInferenceLimit
		logging.Info("Updated inference count limit", types.Config,
			"newLimit", newInferenceLimit, "epoch", currentEpochIndex)
	}

	bl.cachedLimitEpochIndex = currentEpochIndex
}

func (bl *BandwidthLimiter) calculateUniformLimits(currentEpochIndex uint64) (uint64, uint64) {
	epochGroupData, err := bl.epochCache.GetCurrentEpochGroupData(currentEpochIndex)
	if err != nil {
		logging.Warn("Failed to get epoch data, using default limits", types.Config, "error", err)
		return bl.defaultLimit, bl.defaultInferenceLimit
	}

	participantCount := uint64(len(epochGroupData.ValidationWeights))
	if participantCount == 0 {
		return bl.defaultLimit, bl.defaultInferenceLimit
	}

	kbLimit := bl.defaultLimit / participantCount
	inferenceLimit := bl.defaultInferenceLimit / participantCount
	if inferenceLimit == 0 && bl.defaultInferenceLimit > 0 {
		inferenceLimit = 1 // Minimum of 1 inference per block per node
	}

	return kbLimit, inferenceLimit
}

// calculateUniformLimit is kept for backward compatibility
func (bl *BandwidthLimiter) calculateUniformLimit(currentEpochIndex uint64) uint64 {
	kbLimit, _ := bl.calculateUniformLimits(currentEpochIndex)
	return kbLimit
}

func (bl *BandwidthLimiter) RecordRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] += estimatedKB
	bl.inferencesPerBlock[completionBlock]++
}

func (bl *BandwidthLimiter) ReleaseRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] -= estimatedKB
	bl.inferencesPerBlock[completionBlock]--

	if bl.usagePerBlock[completionBlock] <= 0 {
		delete(bl.usagePerBlock, completionBlock)
	}
	if bl.inferencesPerBlock[completionBlock] <= 0 {
		delete(bl.inferencesPerBlock, completionBlock)
	}
}

func (bl *BandwidthLimiter) startCleanupRoutine() {
	ticker := time.NewTicker(bl.cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		bl.cleanupOldEntries()
	}
}

func (bl *BandwidthLimiter) cleanupOldEntries() {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	// Find newest block across both maps
	var newestBlock int64
	for block := range bl.usagePerBlock {
		if block > newestBlock {
			newestBlock = block
		}
	}
	for block := range bl.inferencesPerBlock {
		if block > newestBlock {
			newestBlock = block
		}
	}

	if newestBlock == 0 {
		return
	}

	cutoffBlock := newestBlock - bl.requestLifespanBlocks*2 // Keep some buffer

	// Cleanup KB usage map
	for block := range bl.usagePerBlock {
		if block < cutoffBlock {
			delete(bl.usagePerBlock, block)
		}
	}

	// Cleanup inference count map
	for block := range bl.inferencesPerBlock {
		if block < cutoffBlock {
			delete(bl.inferencesPerBlock, block)
		}
	}
}

func NewBandwidthLimiterFromConfig(configManager ConfigManager, recorder cosmosclient.CosmosMessageClient, phaseTracker ChainPhaseTracker) *BandwidthLimiter {
	validationParams := configManager.GetValidationParams()
	bandwidthParams := configManager.GetBandwidthParams()

	requestLifespanBlocks := validationParams.ExpirationBlocks
	if requestLifespanBlocks == 0 {
		requestLifespanBlocks = 10
	}

	limitsPerBlockKB := bandwidthParams.EstimatedLimitsPerBlockKb
	if limitsPerBlockKB == 0 {
		limitsPerBlockKB = 21 * 1024 // 21MB default
	}

	kbPerInputToken := bandwidthParams.KbPerInputToken
	if kbPerInputToken == 0 {
		kbPerInputToken = 0.0023
	}

	kbPerOutputToken := bandwidthParams.KbPerOutputToken
	if kbPerOutputToken == 0 {
		kbPerOutputToken = 0.64
	}

	maxInferencesPerBlock := bandwidthParams.MaxInferencesPerBlock

	bl := &BandwidthLimiter{
		limitsPerBlockKB:      limitsPerBlockKB,
		usagePerBlock:         make(map[int64]float64),
		cleanupInterval:       30 * time.Second,
		requestLifespanBlocks: requestLifespanBlocks,
		kbPerInputToken:       kbPerInputToken,
		kbPerOutputToken:      kbPerOutputToken,
		inferencesPerBlock:    make(map[int64]int64),
		maxInferencesPerBlock: maxInferencesPerBlock,
		defaultInferenceLimit: maxInferencesPerBlock,
		recorder:              recorder,
		defaultLimit:          limitsPerBlockKB,
		phaseTracker:          phaseTracker,
		configManager:         configManager,
	}

	if recorder != nil && phaseTracker != nil {
		bl.epochCache = NewEpochGroupDataCache(recorder)
	}

	logging.Info("Bandwidth limiter initialized", types.Config,
		"limit", limitsPerBlockKB, "lifespan", requestLifespanBlocks,
		"maxInferences", maxInferencesPerBlock,
		"weightBased", recorder != nil && phaseTracker != nil)

	go bl.startCleanupRoutine()
	return bl
}

type ConfigManager interface {
	GetValidationParams() apiconfig.ValidationParamsCache
	GetBandwidthParams() apiconfig.BandwidthParamsCache
}

type ChainPhaseTracker interface {
	GetCurrentEpochState() *chainphase.EpochState
}
