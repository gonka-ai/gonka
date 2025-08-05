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
	usagePerBlock         map[int64]float64 // blockHeight -> estimated KB
	cleanupInterval       time.Duration
	requestLifespanBlocks int64
	// Configurable coefficients from chain parameters
	kbPerInputToken  float64
	kbPerOutputToken float64

	// Simple weight-based allocation with caching
	recorder              cosmosclient.CosmosMessageClient
	defaultLimit          uint64
	epochCache            *EpochGroupDataCache
	phaseTracker          ChainPhaseTracker // for getting current epoch index
	cachedLimitEpochIndex uint64            // epoch index for cached limit
	cachedWeightLimit     uint64            // cached calculated limit
}

// NewBandwidthLimiter creates a new BandwidthLimiter.
func NewBandwidthLimiter(limitsPerBlockKB uint64, requestLifespanBlocks int64, kbPerInputToken, kbPerOutputToken float64) *BandwidthLimiter {
	bl := &BandwidthLimiter{
		limitsPerBlockKB:      limitsPerBlockKB,
		usagePerBlock:         make(map[int64]float64),
		cleanupInterval:       5 * time.Minute,
		requestLifespanBlocks: requestLifespanBlocks,
		kbPerInputToken:       kbPerInputToken,
		kbPerOutputToken:      kbPerOutputToken,
	}
	go bl.startCleanupRoutine()
	return bl
}

// NewBandwidthLimiterWithWeights creates a BandwidthLimiter with weight-based allocation
func NewBandwidthLimiterWithWeights(recorder cosmosclient.CosmosMessageClient, defaultLimit uint64, requestLifespanBlocks int64, kbPerInputToken, kbPerOutputToken float64, phaseTracker ChainPhaseTracker) *BandwidthLimiter {
	// Start with default limit - will be updated on first request
	bl := &BandwidthLimiter{
		limitsPerBlockKB:      defaultLimit,
		usagePerBlock:         make(map[int64]float64),
		cleanupInterval:       5 * time.Minute,
		requestLifespanBlocks: requestLifespanBlocks,
		kbPerInputToken:       kbPerInputToken,
		kbPerOutputToken:      kbPerOutputToken,
		recorder:              recorder,
		defaultLimit:          defaultLimit,
		epochCache:            NewEpochGroupDataCache(recorder),
		phaseTracker:          phaseTracker,
	}
	go bl.startCleanupRoutine()
	return bl
}

// CanAcceptRequest checks if a new request can be accepted based on estimated bandwidth.
func (bl *BandwidthLimiter) CanAcceptRequest(blockHeight int64, promptTokens, maxTokens int) (bool, float64) {
	// Update limits if we have weight-based allocation enabled
	bl.maybeUpdateLimits()

	bl.mu.RLock()
	defer bl.mu.RUnlock()

	estimatedKB := float64(promptTokens)*bl.kbPerInputToken + float64(maxTokens)*bl.kbPerOutputToken

	totalUsage := 0.0
	for i := blockHeight; i < blockHeight+bl.requestLifespanBlocks; i++ {
		totalUsage += bl.usagePerBlock[i]
	}
	avgUsage := totalUsage / float64(bl.requestLifespanBlocks)
	estimatedKBPerBlock := estimatedKB / float64(bl.requestLifespanBlocks)

	logging.Info("CanAcceptRequest", types.Config,
		"avgUsage", avgUsage,
		"estimatedKBPerBlock", estimatedKBPerBlock,
		"limitsPerBlockKB", bl.limitsPerBlockKB,
		"requestLifespanBlocks", bl.requestLifespanBlocks,
		"totalUsage", totalUsage)

	return avgUsage+estimatedKBPerBlock <= float64(bl.limitsPerBlockKB), estimatedKB
}

func (bl *BandwidthLimiter) maybeUpdateLimits() {
	if bl.epochCache == nil || bl.phaseTracker == nil {
		return
	}

	epochState := bl.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		return
	}

	if newLimit, err := bl.calculateWeightBasedLimitFromCache(epochState.LatestEpoch.EpochIndex); err == nil {
		bl.mu.Lock()
		if bl.limitsPerBlockKB != newLimit {
			oldLimit := bl.limitsPerBlockKB
			bl.limitsPerBlockKB = newLimit
			logging.Info("Updated bandwidth limit from cached epoch data", types.Config,
				"oldLimit", oldLimit, "newLimit", newLimit, "epochIndex", epochState.LatestEpoch.EpochIndex)
		}
		bl.mu.Unlock()
	}
}

func (bl *BandwidthLimiter) calculateWeightBasedLimitFromCache(currentEpochIndex uint64) (uint64, error) {
	// Check if we already have cached limit for this epoch
	if bl.cachedLimitEpochIndex == currentEpochIndex && bl.cachedWeightLimit > 0 {
		return bl.cachedWeightLimit, nil
	}

	epochGroupData, err := bl.epochCache.GetCurrentEpochGroupData(currentEpochIndex)
	if err != nil {
		logging.Warn("Failed to get cached epoch group data, using default limit", types.Config, "error", err)
		return bl.defaultLimit, nil
	}

	if len(epochGroupData.ValidationWeights) == 0 {
		logging.Warn("No validation weights found, using default limit", types.Config)
		return bl.defaultLimit, nil
	}

	currentNodeAddress := bl.recorder.GetAccountAddress()
	var nodeWeight int64 = 0
	var totalWeight int64 = 0

	// Simple loop - only current epoch's active participants
	for _, validationWeight := range epochGroupData.ValidationWeights {
		totalWeight += validationWeight.Weight
		if validationWeight.MemberAddress == currentNodeAddress {
			nodeWeight = validationWeight.Weight
		}
	}

	// Simple validation
	if totalWeight <= 0 || nodeWeight <= 0 {
		logging.Warn("Invalid weights, using default limit", types.Config,
			"nodeWeight", nodeWeight, "totalWeight", totalWeight)
		return bl.defaultLimit, nil
	}

	// Simple calculation
	adjustedLimit := uint64(float64(bl.defaultLimit) * float64(nodeWeight) / float64(totalWeight))

	// Cache the calculated limit
	bl.cachedLimitEpochIndex = currentEpochIndex
	bl.cachedWeightLimit = adjustedLimit

	logging.Info("Calculated weight-based limit using cached epoch data", types.Config,
		"nodeWeight", nodeWeight, "totalWeight", totalWeight,
		"defaultLimit", bl.defaultLimit, "adjustedLimit", adjustedLimit,
		"activeParticipants", len(epochGroupData.ValidationWeights),
		"epochIndex", currentEpochIndex)

	return adjustedLimit, nil
}

// RecordRequest records the estimated bandwidth usage at the completion block.
func (bl *BandwidthLimiter) RecordRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] += estimatedKB
	logging.Info("Recorded request", types.Config, "startBlockHeight", startBlockHeight, "estimatedKB", estimatedKB, "completionBlock", completionBlock)
}

// ReleaseRequest subtracts the estimated bandwidth usage from the completion block.
func (bl *BandwidthLimiter) ReleaseRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] -= estimatedKB

	logging.Info("Released request", types.Config, "startBlockHeight", startBlockHeight, "estimatedKB", estimatedKB, "completionBlock", completionBlock)

	if bl.usagePerBlock[completionBlock] <= 0 {
		delete(bl.usagePerBlock, completionBlock)
	}
}

// startCleanupRoutine periodically removes old entries from the usage map.
func (bl *BandwidthLimiter) startCleanupRoutine() {
	ticker := time.NewTicker(bl.cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		bl.cleanupOldEntries()
	}
}

// cleanupOldEntries removes entries older than the cleanup threshold.
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

// NewBandwidthLimiterFromConfig creates a BandwidthLimiter with config parameters
// MINIMALISTIC: Simple configuration loading with optional weight-based allocation
func NewBandwidthLimiterFromConfig(configManager ConfigManager, recorder cosmosclient.CosmosMessageClient, phaseTracker ChainPhaseTracker) *BandwidthLimiter {
	validationParams := configManager.GetValidationParams()
	bandwidthParams := configManager.GetBandwidthParams()

	requestLifespanBlocks := validationParams.ExpirationBlocks
	if requestLifespanBlocks == 0 {
		requestLifespanBlocks = 10
	}

	limitsPerBlockKB := bandwidthParams.EstimatedLimitsPerBlockKb
	if limitsPerBlockKB == 0 {
		limitsPerBlockKB = 1024
	}

	kbPerInputToken := bandwidthParams.KbPerInputToken
	if kbPerInputToken == 0 {
		kbPerInputToken = 0.0023
	}

	kbPerOutputToken := bandwidthParams.KbPerOutputToken
	if kbPerOutputToken == 0 {
		kbPerOutputToken = 0.64
	}

	logging.Info("Creating bandwidth limiter", types.Config,
		"limitsPerBlockKB", limitsPerBlockKB,
		"requestLifespanBlocks", requestLifespanBlocks,
		"kbPerInputToken", kbPerInputToken,
		"kbPerOutputToken", kbPerOutputToken,
		"weightBasedAllocation", recorder != nil)

	if recorder != nil && phaseTracker != nil {
		return NewBandwidthLimiterWithWeights(recorder, limitsPerBlockKB, requestLifespanBlocks, kbPerInputToken, kbPerOutputToken, phaseTracker)
	} else {
		return NewBandwidthLimiter(limitsPerBlockKB, requestLifespanBlocks, kbPerInputToken, kbPerOutputToken)
	}
}

type ConfigManager interface {
	GetValidationParams() apiconfig.ValidationParamsCache
	GetBandwidthParams() apiconfig.BandwidthParamsCache
}

type ChainPhaseTracker interface {
	GetCurrentEpochState() *chainphase.EpochState
}
