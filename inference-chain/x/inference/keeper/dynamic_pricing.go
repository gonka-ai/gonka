package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// DynamicPricingKeeper contains the functions for dynamic pricing calculations
// This file centralizes all pricing logic to keep other files focused on their primary responsibilities

// UpdateDynamicPricing calculates and updates per-model pricing based on utilization
// Called from BeginBlocker to ensure prices are calculated once per block
func (k *Keeper) UpdateDynamicPricing(ctx context.Context) error {
	// Get current parameters
	params := k.GetParams(ctx)
	if params.DynamicPricingParams == nil {
		return fmt.Errorf("dynamic pricing parameters not found")
	}

	dpParams := params.DynamicPricingParams

	// Get current epoch to check if we're in grace period
	currentEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		return fmt.Errorf("effective epoch not found")
	}

	// Get all active models from current epoch group (needed for both grace period and normal pricing)
	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current epoch group: %w", err)
	}

	mainEpochData := currentEpochGroup.GroupData
	if mainEpochData == nil {
		return fmt.Errorf("epoch group data is nil")
	}

	// TODO: Check if we can optimize it by reading from cached models capacity
	models := mainEpochData.SubGroupModels

	// Handle grace period (active and transition)
	if currentEpoch.Index <= dpParams.GracePeriodEndEpoch {
		k.handleGracePeriod(ctx, currentEpoch, dpParams, models)
		return nil
	}

	// Calculate time window for utilization
	blockTime := sdk.UnwrapSDKContext(ctx).BlockTime()
	currentTimeMillis := blockTime.UnixMilli()                         // Current time in milliseconds
	windowDurationSeconds := int64(dpParams.UtilizationWindowDuration) // Window duration in seconds (e.g., 60)
	windowDurationMillis := windowDurationSeconds * 1000               // Convert to milliseconds for time queries
	timeWindowStartMillis := currentTimeMillis - windowDurationMillis  // Start time in milliseconds

	k.LogInfo("Starting dynamic pricing update", types.Pricing,
		"currentTime", currentTimeMillis, "windowStart", timeWindowStartMillis, "windowDuration", windowDurationMillis)

	// Get utilization stats for all models over the time window (using milliseconds)
	statsMap := k.GetSummaryByModelAndTime(ctx, timeWindowStartMillis, currentTimeMillis)

	totalModelsProcessed := 0
	totalPriceChanges := 0

	// Process each active model
	for _, modelId := range models {
		// Get cached capacity for this model
		capacity, err := k.GetCachedModelCapacity(ctx, modelId)
		if err != nil {
			k.LogWarn("Failed to get cached capacity for model, skipping", types.Pricing,
				"modelId", modelId, "error", err)
			continue
		}

		// Get utilization stats for this model
		stats, hasStats := statsMap[modelId]
		tokensUsed := int64(0)
		if hasStats {
			tokensUsed = stats.TokensUsed
		}

		// Calculate utilization (0.0 to 1.0+)
		// capacity is tokens/second, so scale it to the window duration in seconds
		utilization := 0.0
		if capacity > 0 {
			capacityForWindow := float64(capacity) * float64(windowDurationSeconds) // capacity * seconds = total tokens
			utilization = float64(tokensUsed) / capacityForWindow
		}

		k.LogInfo("Model utilization calculated", types.Pricing,
			"modelId", modelId, "tokensUsed", tokensUsed, "capacityPerSec", capacity,
			"windowDuration", windowDurationMillis, "utilization", utilization)

		// Calculate new price using our algorithm
		oldPrice, newPrice, err := k.CalculateModelDynamicPrice(ctx, modelId, utilization)
		if err != nil {
			k.LogError("Failed to calculate dynamic price for model", types.Pricing,
				"modelId", modelId, "error", err)
			continue
		}

		// Update the price in KV storage
		err = k.SetModelCurrentPrice(ctx, modelId, newPrice)
		if err != nil {
			k.LogError("Failed to update price for model", types.Pricing,
				"modelId", modelId, "newPrice", newPrice, "error", err)
			continue
		}

		// Track changes
		totalModelsProcessed++
		if newPrice != oldPrice {
			totalPriceChanges++
		}

		k.LogInfo("Updated model price", types.Pricing,
			"modelId", modelId, "oldPrice", oldPrice, "newPrice", newPrice,
			"utilization", utilization, "changed", newPrice != oldPrice)
	}

	k.LogInfo("Completed dynamic pricing update", types.Pricing,
		"totalModels", len(mainEpochData.SubGroupModels), "modelsProcessed", totalModelsProcessed,
		"priceChanges", totalPriceChanges, "windowDuration", windowDurationMillis)

	return nil
}

// CalculateModelDynamicPrice implements the stability zone price adjustment algorithm
// Returns the new per-token price for a specific model based on utilization
func (k *Keeper) CalculateModelDynamicPrice(ctx context.Context, modelId string, utilization float64) (uint64, uint64, error) {
	// Get current parameters
	params := k.GetParams(ctx)
	if params.DynamicPricingParams == nil {
		return 0, 0, fmt.Errorf("dynamic pricing parameters not found")
	}

	dpParams := params.DynamicPricingParams

	// Note: Grace period is checked globally in UpdateDynamicPricing()
	// so this function is only called when grace period has ended

	// Get current price for this model
	currentPrice, err := k.GetModelCurrentPrice(ctx, modelId)
	if err != nil {
		// If no current price exists, use base price
		currentPrice = dpParams.BasePerTokenPrice
		k.LogInfo("Using base price for model with no current price", types.Pricing,
			"modelId", modelId, "basePrice", currentPrice)
	}

	// Extract parameters
	lowerBound := dpParams.StabilityZoneLowerBound.ToFloat()
	upperBound := dpParams.StabilityZoneUpperBound.ToFloat()
	elasticity := dpParams.PriceElasticity.ToFloat()
	minPrice := dpParams.MinPerTokenPrice

	// Growth caps derived from elasticity parameter and stability zone bounds (governance-configurable)
	// Calculate maximum possible deviations from stability zone dynamically
	// Maximum excess: from upperBound to 100% utilization (for price increases)
	// Maximum deficit: from lowerBound to 0% utilization (for price decreases)
	maxExcessDeviation := 1.0 - upperBound  // e.g., 1.0 - 0.60 = 0.40
	maxDeficitDeviation := lowerBound - 0.0 // e.g., 0.40 - 0.0 = 0.40

	// Use appropriate deviation for each scenario
	maxIncreasePerBlock := 1.0 + (maxExcessDeviation * elasticity)  // e.g., 1.0 + (0.40 × 0.05) = 1.02
	maxDecreasePerBlock := 1.0 - (maxDeficitDeviation * elasticity) // e.g., 1.0 - (0.40 × 0.05) = 0.98

	var newPrice uint64

	// Stability zone check (40%-60% by default)
	if utilization >= lowerBound && utilization <= upperBound {
		// Stability zone - no price change
		newPrice = currentPrice
		k.LogInfo("Price unchanged - within stability zone", types.Pricing,
			"modelId", modelId, "utilization", utilization, "price", newPrice)
	} else if utilization < lowerBound {
		// Below stability zone - decrease price (with cap)
		utilizationDeficit := lowerBound - utilization
		adjustmentFactor := 1.0 - (utilizationDeficit * elasticity)

		// Ensure adjustment factor doesn't go negative or below max decrease cap
		if adjustmentFactor < 0 {
			adjustmentFactor = 0
		}
		// Apply maximum decrease cap (2% per block)
		if adjustmentFactor < maxDecreasePerBlock {
			adjustmentFactor = maxDecreasePerBlock
		}

		newPriceFloat := float64(currentPrice) * adjustmentFactor
		newPrice = uint64(newPriceFloat)

		k.LogInfo("Price decreased - below stability zone", types.Pricing,
			"modelId", modelId, "utilization", utilization, "deficit", utilizationDeficit,
			"adjustmentFactor", adjustmentFactor, "oldPrice", currentPrice, "newPrice", newPrice)
	} else {
		// Above stability zone - increase price (with cap)
		utilizationExcess := utilization - upperBound
		adjustmentFactor := 1.0 + (utilizationExcess * elasticity)

		// Apply maximum increase cap (2% per block)
		if adjustmentFactor > maxIncreasePerBlock {
			adjustmentFactor = maxIncreasePerBlock
		}

		newPriceFloat := float64(currentPrice) * adjustmentFactor
		newPrice = uint64(newPriceFloat)

		k.LogInfo("Price increased - above stability zone", types.Pricing,
			"modelId", modelId, "utilization", utilization, "excess", utilizationExcess,
			"adjustmentFactor", adjustmentFactor, "oldPrice", currentPrice, "newPrice", newPrice)
	}

	// Enforce minimum price floor
	if newPrice < minPrice {
		k.LogInfo("Enforcing minimum price floor", types.Pricing,
			"modelId", modelId, "calculatedPrice", newPrice, "minPrice", minPrice)
		newPrice = minPrice
	}

	return currentPrice, newPrice, nil
}

// handleGracePeriod handles both active grace period and transition out of grace period
// This unified function manages pricing during the grace period and the transition to dynamic pricing
func (k *Keeper) handleGracePeriod(ctx context.Context, currentEpoch *types.Epoch, dpParams *types.DynamicPricingParams, subGroupModels []string) {
	var targetPrice uint64
	var priceType, actionDesc string

	if currentEpoch.Index < dpParams.GracePeriodEndEpoch {
		// Grace period is still active - use configurable grace period price
		targetPrice = dpParams.GracePeriodPerTokenPrice
		priceType = "grace"
		actionDesc = "Grace period active - setting all model prices to grace period price"
	} else {
		// Grace period is ending - use base price
		targetPrice = dpParams.BasePerTokenPrice
		priceType = "base"
		actionDesc = "Grace period ending - initializing base pricing for all models"
	}

	k.LogInfo(actionDesc, types.Pricing,
		"currentEpoch", currentEpoch.Index, "gracePeriodEndEpoch", dpParams.GracePeriodEndEpoch,
		"targetPrice", targetPrice, "totalModels", len(subGroupModels))

	// Set target price for all models
	for _, modelId := range subGroupModels {
		err := k.SetModelCurrentPrice(ctx, modelId, targetPrice)
		if err != nil {
			k.LogError("Failed to set price for model during grace period", types.Pricing,
				"modelId", modelId, "priceType", priceType, "targetPrice", targetPrice, "error", err)
			continue
		}
		k.LogInfo("Set grace period price", types.Pricing,
			"modelId", modelId, "priceType", priceType, "price", targetPrice)
	}
}

// RecordInferencePrice locks in the current price for an inference
// Called only on the first message (Start or Finish) to ensure consistent pricing
// BeginBlocker must have set prices before this is called
func (k *Keeper) RecordInferencePrice(
	ctx context.Context,
	inference *types.Inference,
	inferenceId string,
) {
	if inference == nil {
		return
	}
	if inference.Model == "" {
		k.LogError("RecordInferencePrice called with empty model ID", types.Pricing,
			"inferenceId", inference.InferenceId, "inference", inference)
	}
	// Fast path: check if price is already stored (already locked in)
	if inference.PerTokenPrice > 0 {
		return // Already recorded, nothing to do
	}

	// Price not yet recorded - read pre-calculated price from BeginBlocker
	currentPrice, err := k.GetModelCurrentPrice(ctx, inference.Model)
	if err != nil {
		// This should never happen if BeginBlocker ran properly
		// Log error but don't fail the inference - use legacy price as emergency fallback
		k.LogError("Failed to get current price - BeginBlocker may not have run", types.Pricing,
			"inferenceId", inferenceId, "modelId", inference.Model, "error", err)
		// Use legacy pricing as fallback (same value as calculations.PerTokenCost)
		currentPrice = calculations.PerTokenCost
	}

	// Always ensure PerTokenPrice is set to a valid value (including 0 for grace period)
	// This eliminates the need for complex fallback logic in calculation functions
	inference.PerTokenPrice = currentPrice

	k.LogInfo("Recorded inference price", types.Pricing,
		"inferenceId", inferenceId, "modelId", inference.Model, "lockedPrice", currentPrice)
}

// Model Capacity Caching Functions

// CacheModelCapacity stores a model's capacity in KV storage for fast access
func (k *Keeper) CacheModelCapacity(ctx context.Context, modelId string, capacity int64) error {
	if capacity < 0 {
		return fmt.Errorf("capacity cannot be negative: %d", capacity)
	}
	// Convert int64 to uint64 for storage with Collections
	return k.ModelCapacityMap.Set(ctx, modelId, uint64(capacity))
}

// GetCachedModelCapacity retrieves a model's cached capacity from KV storage
func (k *Keeper) GetCachedModelCapacity(ctx context.Context, modelId string) (int64, error) {
	value, err := k.ModelCapacityMap.Get(ctx, modelId)
	if err != nil {
		return 0, fmt.Errorf("capacity not found for model: %s", modelId)
	}
	// Convert uint64 back to int64
	return int64(value), nil
}

// CacheAllModelCapacities caches capacity for all active models during epoch activation
func (k *Keeper) CacheAllModelCapacities(ctx context.Context) error {
	// Get the current epoch group to access all models
	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current epoch group: %w", err)
	}

	// Get the main epoch group data
	mainEpochData := currentEpochGroup.GroupData
	if mainEpochData == nil {
		return fmt.Errorf("epoch group data is nil")
	}

	// Cache capacity for each sub-model
	for _, modelId := range mainEpochData.SubGroupModels {
		// Get the epoch group data for this specific model
		modelEpochData, found := k.GetEpochGroupData(ctx, mainEpochData.EpochIndex, modelId)
		if !found {
			k.LogWarn("Sub epoch data not found during capacity caching", types.Pricing,
				"modelId", modelId, "epoch_index", mainEpochData.EpochIndex)
			continue
		}

		// TODO: The proposal mentions copying from a `total_throughput` field, but this field
		// doesn't exist in the current EpochGroupData structure. For now, we use TotalWeight
		// as a proxy for capacity (tokens per second), as 1000 nonce of PoC produce aproximetely
		// 1000 tokens for of QwQ-32B model. In a future task, we need to:
		// 1. Add `total_throughput` field to EpochGroupData proto
		// 2. Update this function to use the actual throughput data (tokens/second)
		// 3. Implement logic to calculate/set throughput during epoch formation
		capacity := modelEpochData.TotalWeight
		if capacity <= 0 {
			// Set a reasonable default capacity for models with no weight
			capacity = 1000 // 1K tokens per second as default
			k.LogWarn("Using default capacity for model with zero total weight", types.Pricing,
				"modelId", modelId, "defaultCapacityPerSec", capacity)
		}

		// Cache the capacity for this model
		err := k.CacheModelCapacity(ctx, modelId, capacity)
		if err != nil {
			k.LogError("Failed to cache model capacity", types.Pricing,
				"modelId", modelId, "capacity", capacity, "error", err)
			continue
		}

		k.LogInfo("Cached model capacity", types.Pricing,
			"modelId", modelId, "capacity", capacity)
	}

	k.LogInfo("Completed caching capacities for all models", types.Pricing,
		"totalModels", len(mainEpochData.SubGroupModels))

	return nil
}

// KV Storage Functions for Current Prices

// SetModelCurrentPrice stores the current per-token price for a model
func (k *Keeper) SetModelCurrentPrice(ctx context.Context, modelId string, price uint64) error {
	return k.ModelCurrentPriceMap.Set(ctx, modelId, price)
}

// GetModelCurrentPrice retrieves the current per-token price for a model
func (k *Keeper) GetModelCurrentPrice(ctx context.Context, modelId string) (uint64, error) {
	price, err := k.ModelCurrentPriceMap.Get(ctx, modelId)
	if err != nil {
		return 0, fmt.Errorf("current price not found for model: %s", modelId)
	}
	return price, nil
}

// GetAllModelCurrentPrices retrieves current prices for all models using Collections
func (k *Keeper) GetAllModelCurrentPrices(ctx context.Context) (map[string]uint64, error) {
	result := make(map[string]uint64)
	if err := k.ModelCurrentPriceMap.Walk(ctx, nil, func(modelId string, price uint64) (bool, error) {
		result[modelId] = price
		return false, nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}
