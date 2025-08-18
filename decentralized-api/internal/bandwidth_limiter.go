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

	recorder              cosmosclient.CosmosMessageClient
	defaultLimit          uint64
	epochCache            *EpochGroupDataCache
	phaseTracker          ChainPhaseTracker
	configManager         ConfigManager
	cachedLimitEpochIndex uint64
	cachedWeightLimit     uint64
}

func (bl *BandwidthLimiter) CanAcceptRequest(blockHeight int64, promptTokens, maxTokens int) (bool, float64) {
	bl.maybeUpdateLimits()

	bl.mu.RLock()
	defer bl.mu.RUnlock()

	estimatedKB := float64(promptTokens)*bl.kbPerInputToken + float64(maxTokens)*bl.kbPerOutputToken

	totalUsage := 0.0
	windowSize := bl.requestLifespanBlocks + 1
	for i := blockHeight; i <= blockHeight+bl.requestLifespanBlocks; i++ {
		totalUsage += bl.usagePerBlock[i]
	}

	avgUsage := totalUsage / float64(windowSize)
	estimatedKBPerBlock := estimatedKB / float64(windowSize)
	canAccept := avgUsage+estimatedKBPerBlock <= float64(bl.limitsPerBlockKB)

	logging.Debug("CanAcceptRequest", types.Config,
		"avgUsage", avgUsage,
		"estimatedKB", estimatedKBPerBlock,
		"limitsPerBlockKB", bl.limitsPerBlockKB,
		"requestLifespanBlocks", bl.requestLifespanBlocks,
		"totalUsage", totalUsage)

	if !canAccept {
		logging.Info("Bandwidth limit exceeded", types.Config,
			"avgUsage", avgUsage, "estimatedKB", estimatedKBPerBlock, "limit", bl.limitsPerBlockKB)
	}

	return canAccept, estimatedKB
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
	if bl.cachedLimitEpochIndex == currentEpochIndex {
		return
	}

	if bl.configManager != nil {
		bl.updateParametersFromConfig()
	}

	bl.updateWeightBasedLimit(currentEpochIndex)
}

func (bl *BandwidthLimiter) updateParametersFromConfig() {
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

	if updated {
		logging.Info("Updated bandwidth parameters from config", types.Config,
			"lifespanBlocks", bl.requestLifespanBlocks,
			"kbPerInputToken", bl.kbPerInputToken,
			"kbPerOutputToken", bl.kbPerOutputToken,
			"defaultLimit", bl.defaultLimit)
	}
}

func (bl *BandwidthLimiter) updateWeightBasedLimit(currentEpochIndex uint64) {
	if bl.epochCache == nil || bl.recorder == nil {
		logging.Warn("Epoch cache or recorder is nil, skipping weight-based limit update", types.Config)
		return
	}

	if bl.cachedLimitEpochIndex == currentEpochIndex && bl.cachedWeightLimit > 0 {
		return
	}

	newLimit := bl.calculateUniformLimit(currentEpochIndex)

	bl.mu.Lock()
	defer bl.mu.Unlock()

	if bl.limitsPerBlockKB != newLimit {
		bl.limitsPerBlockKB = newLimit
		logging.Info("Updated bandwidth limit", types.Config,
			"newLimit", newLimit, "epoch", currentEpochIndex)
	}
}

func (bl *BandwidthLimiter) calculateUniformLimit(currentEpochIndex uint64) uint64 {
	epochGroupData, err := bl.epochCache.GetCurrentEpochGroupData(currentEpochIndex)
	if err != nil {
		logging.Warn("Failed to get epoch data, using default limit", types.Config, "error", err)
		return bl.defaultLimit
	}

	return bl.defaultLimit / uint64(len(epochGroupData.ValidationWeights))
}

// Weigh based limits. We ignore it for now
func (bl *BandwidthLimiter) calculateWeightBasedLimit(currentEpochIndex uint64) uint64 {
	epochGroupData, err := bl.epochCache.GetCurrentEpochGroupData(currentEpochIndex)
	if err != nil {
		logging.Warn("Failed to get epoch data, using default limit", types.Config, "error", err)
		return bl.defaultLimit
	}

	if len(epochGroupData.ValidationWeights) == 0 {
		return bl.defaultLimit
	}

	nodeAddress := bl.recorder.GetAccountAddress()
	nodeWeight, totalWeight := bl.calculateWeights(epochGroupData.ValidationWeights, nodeAddress)

	if totalWeight <= 0 || nodeWeight <= 0 {
		logging.Warn("Invalid weights, using default limit", types.Config,
			"nodeWeight", nodeWeight, "totalWeight", totalWeight)
		return bl.defaultLimit
	}

	adjustedLimit := uint64(float64(bl.defaultLimit) * float64(nodeWeight) / float64(totalWeight))

	bl.cachedLimitEpochIndex = currentEpochIndex
	bl.cachedWeightLimit = adjustedLimit

	logging.Info("Calculated weight-based limit", types.Config,
		"nodeWeight", nodeWeight, "totalWeight", totalWeight,
		"adjustedLimit", adjustedLimit, "participants", len(epochGroupData.ValidationWeights))

	return adjustedLimit
}

func (bl *BandwidthLimiter) calculateWeights(weights []*types.ValidationWeight, nodeAddress string) (int64, int64) {
	var nodeWeight, totalWeight int64

	for _, weight := range weights {
		totalWeight += weight.Weight
		if weight.MemberAddress == nodeAddress {
			nodeWeight = weight.Weight
		}
	}

	return nodeWeight, totalWeight
}

func (bl *BandwidthLimiter) RecordRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] += estimatedKB
}

func (bl *BandwidthLimiter) ReleaseRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] -= estimatedKB

	if bl.usagePerBlock[completionBlock] <= 0 {
		delete(bl.usagePerBlock, completionBlock)
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

	if len(bl.usagePerBlock) == 0 {
		return
	}

	var newestBlock int64
	for block := range bl.usagePerBlock {
		if block > newestBlock {
			newestBlock = block
		}
	}

	cutoffBlock := newestBlock - bl.requestLifespanBlocks*2 // Keep some buffer
	for block := range bl.usagePerBlock {
		if block < cutoffBlock {
			delete(bl.usagePerBlock, block)
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

	bl := &BandwidthLimiter{
		limitsPerBlockKB:      limitsPerBlockKB,
		usagePerBlock:         make(map[int64]float64),
		cleanupInterval:       30 * time.Second,
		requestLifespanBlocks: requestLifespanBlocks,
		kbPerInputToken:       kbPerInputToken,
		kbPerOutputToken:      kbPerOutputToken,
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
