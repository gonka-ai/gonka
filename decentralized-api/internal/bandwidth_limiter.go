package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// BandwidthLimiter provides a simple mechanism to enforce bandwidth limits.
// It is based on predictive estimation of request size.
type BandwidthLimiter struct {
	mu                    sync.RWMutex
	limitsPerBlockKB      uint64
	usagePerBlock         map[int64]float64 // blockHeight -> estimated KB
	cleanupInterval       time.Duration
	requestLifespanBlocks int64
	// Configurable coefficients from chain parameters
	kbPerInputToken  float64
	kbPerOutputToken float64
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

// CanAcceptRequest checks if a new request can be accepted based on estimated bandwidth.
// It checks the average bandwidth usage across the request lifespan period.
func (bl *BandwidthLimiter) CanAcceptRequest(blockHeight int64, promptTokens, maxTokens int) (bool, float64) {
	bl.mu.RLock()
	defer bl.mu.RUnlock()

	estimatedKB := float64(promptTokens)*bl.kbPerInputToken + float64(maxTokens)*bl.kbPerOutputToken

	totalUsage := 0.0
	for i := blockHeight; i < blockHeight+bl.requestLifespanBlocks; i++ {
		totalUsage += bl.usagePerBlock[i]
	}
	avgUsage := totalUsage / float64(bl.requestLifespanBlocks)
	estimatedKBPerBlock := estimatedKB / float64(bl.requestLifespanBlocks)

	return avgUsage+estimatedKBPerBlock <= float64(bl.limitsPerBlockKB), estimatedKB
}

// RecordRequest records the estimated bandwidth usage at the completion block.
func (bl *BandwidthLimiter) RecordRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] += estimatedKB
}

// ReleaseRequest subtracts the estimated bandwidth usage from the completion block.
func (bl *BandwidthLimiter) ReleaseRequest(startBlockHeight int64, estimatedKB float64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	completionBlock := startBlockHeight + bl.requestLifespanBlocks
	bl.usagePerBlock[completionBlock] -= estimatedKB

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

// CalculateWeightBasedBandwidthLimit calculates the weight-based bandwidth limit for this Transfer Agent
// Formula: taEstimatedLimitsPerBlockKb = EstimatedLimitsPerBlockKb * (nodeWeight / totalWeight)
func CalculateWeightBasedBandwidthLimit(recorder cosmosclient.CosmosMessageClient, defaultLimit uint64) (uint64, error) {
	queryClient := recorder.NewInferenceQueryClient()

	response, err := queryClient.ParticipantAll(context.Background(), &types.QueryAllParticipantRequest{})
	if err != nil {
		return defaultLimit, err
	}

	currentNodeAddress := recorder.GetAccountAddress()
	var nodeWeight int32 = 0
	var totalWeight int32 = 0

	for _, participant := range response.Participant {
		totalWeight += participant.Weight
		if participant.Address == currentNodeAddress {
			nodeWeight = participant.Weight
		}
	}

	if nodeWeight == 0 || totalWeight == 0 {
		logging.Warn("Node weight not found or zero, using default bandwidth limit", types.Config,
			"nodeAddress", currentNodeAddress, "nodeWeight", nodeWeight, "totalWeight", totalWeight)
		return defaultLimit, nil
	}

	adjustedLimit := uint64(float64(defaultLimit) * float64(nodeWeight) / float64(totalWeight))

	logging.Info("Applied weight-based bandwidth allocation", types.Config,
		"nodeAddress", currentNodeAddress,
		"nodeWeight", nodeWeight,
		"totalWeight", totalWeight,
		"originalLimit", defaultLimit,
		"adjustedLimit", adjustedLimit)

	return adjustedLimit, nil
}
