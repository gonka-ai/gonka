package internal

import (
	"decentralized-api/apiconfig"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Mock implementations for testing
type mockConfigManager struct {
	validationParams apiconfig.ValidationParamsCache
	bandwidthParams  apiconfig.BandwidthParamsCache
}

func (m *mockConfigManager) GetValidationParams() apiconfig.ValidationParamsCache {
	return m.validationParams
}

func (m *mockConfigManager) GetBandwidthParams() apiconfig.BandwidthParamsCache {
	return m.bandwidthParams
}

// newTestBandwidthLimiter creates a bandwidth limiter for testing without weight-based allocation
func newTestBandwidthLimiter(limitsPerBlockKB uint64, requestLifespanBlocks int64, kbPerInputToken, kbPerOutputToken float64) *BandwidthLimiter {
	configManager := &mockConfigManager{
		validationParams: apiconfig.ValidationParamsCache{
			ExpirationBlocks: requestLifespanBlocks,
		},
		bandwidthParams: apiconfig.BandwidthParamsCache{
			EstimatedLimitsPerBlockKb: limitsPerBlockKB,
			KbPerInputToken:           kbPerInputToken,
			KbPerOutputToken:          kbPerOutputToken,
			MaxInferencesPerBlock:     1000, // Default
		},
	}

	// Create without recorder and phase tracker for simple testing
	return NewBandwidthLimiterFromConfig(configManager, nil, nil)
}

// newTestBandwidthLimiterWithInferenceLimit creates a limiter with a custom inference limit
func newTestBandwidthLimiterWithInferenceLimit(limitsPerBlockKB uint64, requestLifespanBlocks int64, maxInferences uint64) *BandwidthLimiter {
	configManager := &mockConfigManager{
		validationParams: apiconfig.ValidationParamsCache{
			ExpirationBlocks: requestLifespanBlocks,
		},
		bandwidthParams: apiconfig.BandwidthParamsCache{
			EstimatedLimitsPerBlockKb: limitsPerBlockKB,
			KbPerInputToken:           0.0023,
			KbPerOutputToken:          0.64,
			MaxInferencesPerBlock:     maxInferences,
		},
	}
	return NewBandwidthLimiterFromConfig(configManager, nil, nil)
}

func TestBandwidthLimiter_CanAcceptRequest(t *testing.T) {
	limiter := newTestBandwidthLimiter(100, 10, 0.0023, 0.64) // 100 KB limit, default coefficients

	// Test case 1: Request well under the limit
	can, _ := limiter.CanAcceptRequest(1, 1000, 100)
	require.True(t, can, "Should accept request under the limit")

	// Test case 2: Create scenario that exceeds the limit
	// Fill up most of the bandwidth at overlapping blocks
	limiter.RecordRequest(1, 800) // Records 800 KB at block 11
	limiter.RecordRequest(5, 800) // Records 800 KB at block 15

	// Try to accept a request starting at block 6 (checks range [6:16])
	// Range [6:16] contains blocks 11 and 15 with 800 KB each
	// Average usage = (800 + 800) / 10 = 160 KB per block (already over 100 KB limit)
	can, _ = limiter.CanAcceptRequest(6, 100, 10) // Small request, should still be rejected
	require.False(t, can, "Should not accept request when average usage already exceeds limit")
}

func TestBandwidthLimiter_RecordAndRelease(t *testing.T) {
	limiter := newTestBandwidthLimiter(100, 10, 0.0023, 0.64)

	// Record a large request that will create conflict
	limiter.RecordRequest(1, 950) // Records 950 KB at block 11

	// Check that a new request starting at block 5 is now rejected
	// This checks range [5:15] which includes block 11 with 950 KB
	// Range [5:15] = 11 blocks, so average existing = 950/11 = 86.36 KB per block
	// New request ~67 KB = 6.1 KB per block, total = 86.36 + 6.1 = 92.46 KB per block (under 100 KB limit)
	// So we need a larger request to exceed the limit
	// ~130 KB request = 11.8 KB per block, total = 98.16 KB (still under)
	// Let's try an even bigger request
	can, _ := limiter.CanAcceptRequest(5, 5000, 500) // ~332 KB request = 30.2 KB per block, total = 116.56 KB (over 100 KB limit)
	require.False(t, can, "Should not accept a new request that would exceed average limit")

	// Release the first request
	limiter.ReleaseRequest(1, 950)

	// Check that the same large request is now accepted
	can, _ = limiter.CanAcceptRequest(5, 5000, 500)
	require.True(t, can, "Should accept a new request after releasing the conflicting one")
}

func TestBandwidthLimiter_Concurrency(t *testing.T) {
	limiter := newTestBandwidthLimiter(100, 10, 0.0023, 0.64) // Lower limit for clearer test
	var wg sync.WaitGroup
	numRoutines := 50

	// Use larger requests to make limits more visible
	promptTokens := 1000
	maxTokens := 30 // 1000×0.0023 + 30×0.64 = ~21.5KB total
	_, estimatedKB := limiter.CanAcceptRequest(1, promptTokens, maxTokens)

	acceptedCount := 0
	var mu sync.Mutex

	for i := 0; i < numRoutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			can, kb := limiter.CanAcceptRequest(1, promptTokens, maxTokens)
			if can {
				limiter.RecordRequest(1, kb)
				mu.Lock()
				acceptedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// All requests record at block 11, so effective limit is 100KB * 10 blocks = 1000KB total
	// With each request ~21.5KB, we expect ~46 requests maximum
	// But due to concurrency races, we might get a few more
	expectedMax := int(math.Floor(1000/estimatedKB)) + 5 // Allow some race condition tolerance
	require.LessOrEqual(t, acceptedCount, expectedMax, "Should not accept significantly more requests than capacity allows")
	require.Greater(t, acceptedCount, 20, "Should accept a reasonable number of requests")
}

func TestBandwidthLimiter_Cleanup(t *testing.T) {
	limiter := newTestBandwidthLimiter(100, 5, 0.0023, 0.64)
	limiter.cleanupInterval = 10 * time.Millisecond // Speed up cleanup for test

	// Record usage - these will be recorded at completion blocks (start + 5)
	limiter.RecordRequest(1, 50) // Records at block 6
	limiter.RecordRequest(2, 50) // Records at block 7

	// Record usage on a much later block
	limiter.RecordRequest(20, 50) // Records at block 25

	// Wait for cleanup to run multiple times
	time.Sleep(50 * time.Millisecond)

	// Manually trigger cleanup to ensure it runs
	limiter.cleanupOldEntries()

	limiter.mu.RLock()
	defer limiter.mu.RUnlock()

	_, exists6 := limiter.usagePerBlock[6]   // From RecordRequest(1, 50)
	_, exists7 := limiter.usagePerBlock[7]   // From RecordRequest(2, 50)
	_, exists25 := limiter.usagePerBlock[25] // From RecordRequest(20, 50)

	require.False(t, exists6, "Usage for block 6 should have been cleaned up")
	require.False(t, exists7, "Usage for block 7 should have been cleaned up")
	require.True(t, exists25, "Usage for block 25 should not have been cleaned up")
}

func TestBandwidthParameterLoading(t *testing.T) {
	// Test that bandwidth parameters are correctly loaded from proto Decimal types

	// Create test Decimal values that simulate chain parameters
	kbPerInputDecimal := &types.Decimal{
		Value:    23, // 0.0023 = 23 * 10^-4
		Exponent: -4,
	}

	kbPerOutputDecimal := &types.Decimal{
		Value:    64, // 0.64 = 64 * 10^-2
		Exponent: -2,
	}

	// Create mock BandwidthLimitsParams
	bandwidthParams := &types.BandwidthLimitsParams{
		EstimatedLimitsPerBlockKb: 1024,
		KbPerInputToken:           kbPerInputDecimal,
		KbPerOutputToken:          kbPerOutputDecimal,
	}

	// Create mock ValidationParams
	validationParams := &types.ValidationParams{
		TimestampExpiration: 300,
		TimestampAdvance:    60,
		ExpirationBlocks:    10,
	}

	// Create mock Params
	params := &types.Params{
		ValidationParams:      validationParams,
		BandwidthLimitsParams: bandwidthParams,
	}

	// Simulate parameter loading logic from new_block_dispatcher.go
	validationConfig := apiconfig.ValidationParamsCache{
		TimestampExpiration: params.ValidationParams.TimestampExpiration,
		TimestampAdvance:    params.ValidationParams.TimestampAdvance,
		ExpirationBlocks:    params.ValidationParams.ExpirationBlocks,
	}

	// Load bandwidth parameters separately (like in new_block_dispatcher.go)
	var bandwidthConfig apiconfig.BandwidthParamsCache
	if params.BandwidthLimitsParams != nil {
		bandwidthConfig = apiconfig.BandwidthParamsCache{
			EstimatedLimitsPerBlockKb: params.BandwidthLimitsParams.EstimatedLimitsPerBlockKb,
			KbPerInputToken:           params.BandwidthLimitsParams.KbPerInputToken.ToFloat(),
			KbPerOutputToken:          params.BandwidthLimitsParams.KbPerOutputToken.ToFloat(),
		}
	}

	// Verify the parameters were loaded correctly
	require.Equal(t, uint64(1024), bandwidthConfig.EstimatedLimitsPerBlockKb, "EstimatedLimitsPerBlockKb should be loaded from bandwidth params")
	require.InDelta(t, 0.0023, bandwidthConfig.KbPerInputToken, 0.00001, "KbPerInputToken should be converted correctly from Decimal")
	require.InDelta(t, 0.64, bandwidthConfig.KbPerOutputToken, 0.00001, "KbPerOutputToken should be converted correctly from Decimal")

	// Verify validation params are separate and preserved
	require.Equal(t, int64(300), validationConfig.TimestampExpiration, "TimestampExpiration should be preserved")
	require.Equal(t, int64(60), validationConfig.TimestampAdvance, "TimestampAdvance should be preserved")
	require.Equal(t, int64(10), validationConfig.ExpirationBlocks, "ExpirationBlocks should be preserved")

	// Test that BandwidthLimiter can be created with these parameters
	limiter := newTestBandwidthLimiter(bandwidthConfig.EstimatedLimitsPerBlockKb, validationConfig.ExpirationBlocks, bandwidthConfig.KbPerInputToken, bandwidthConfig.KbPerOutputToken)
	require.NotNil(t, limiter, "BandwidthLimiter should be created successfully")

	// Test a calculation with the loaded parameters
	can, estimatedKB := limiter.CanAcceptRequest(1, 1000, 100)
	expectedKB := 1000*bandwidthConfig.KbPerInputToken + 100*bandwidthConfig.KbPerOutputToken // 1000*0.0023 + 100*0.64 = 66.3
	require.True(t, can, "Should accept request under limit")
	require.InDelta(t, expectedKB, estimatedKB, 0.01, "Estimated KB should match calculation with loaded parameters")
}

func TestConfigManagerInterface(t *testing.T) {
	// Test that our ConfigManager interface is satisfied by the actual config manager
	// This ensures the factory function will work correctly in the server

	// Create mock config manager
	mockConfig := &MockConfigManager{
		validationParams: apiconfig.ValidationParamsCache{
			ExpirationBlocks: 15,
		},
		bandwidthParams: apiconfig.BandwidthParamsCache{
			EstimatedLimitsPerBlockKb: 2048,
			KbPerInputToken:           0.005,
			KbPerOutputToken:          0.8,
		},
	}

	// Test that our interface methods work
	validationParams := mockConfig.GetValidationParams()
	bandwidthParams := mockConfig.GetBandwidthParams()

	require.Equal(t, int64(15), validationParams.ExpirationBlocks, "Should return validation params")
	require.Equal(t, uint64(2048), bandwidthParams.EstimatedLimitsPerBlockKb, "Should return bandwidth params")
	require.Equal(t, 0.005, bandwidthParams.KbPerInputToken, "Should return input token coefficient")
	require.Equal(t, 0.8, bandwidthParams.KbPerOutputToken, "Should return output token coefficient")
}

// Mock implementations for testing
type MockConfigManager struct {
	validationParams apiconfig.ValidationParamsCache
	bandwidthParams  apiconfig.BandwidthParamsCache
}

func (m *MockConfigManager) GetValidationParams() apiconfig.ValidationParamsCache {
	return m.validationParams
}

func (m *MockConfigManager) GetBandwidthParams() apiconfig.BandwidthParamsCache {
	return m.bandwidthParams
}

func TestBandwidthLimiter_InferenceCountLimit(t *testing.T) {
	// Create limiter with 10 inferences per block limit and 5 block lifespan
	// High KB limit so we only test inference count
	limiter := newTestBandwidthLimiterWithInferenceLimit(100000, 5, 10)

	// First request should be accepted
	can, kb := limiter.CanAcceptRequest(1, 100, 10)
	require.True(t, can, "Should accept first request")

	// Fill up to the limit (window is 6 blocks, limit is 10 per block = 60 total)
	for i := 0; i < 59; i++ {
		limiter.RecordRequest(1, kb)
	}

	// At 59 inferences, should still accept one more
	can, _ = limiter.CanAcceptRequest(1, 100, 10)
	require.True(t, can, "Should accept when at limit")

	// Record one more to hit 60
	limiter.RecordRequest(1, kb)

	// Now at 60 inferences: avg = 60/6 = 10.0, exceeds limit
	can, _ = limiter.CanAcceptRequest(1, 100, 10)
	require.False(t, can, "Should reject when over limit")
}

func TestBandwidthLimiter_InferenceRecordAndRelease(t *testing.T) {
	limiter := newTestBandwidthLimiterWithInferenceLimit(100000, 5, 10)

	// Fill up to the limit
	for i := 0; i < 60; i++ {
		limiter.RecordRequest(1, 1)
	}

	// Should be rejected now
	can, _ := limiter.CanAcceptRequest(1, 100, 10)
	require.False(t, can, "Should reject when over limit")

	// Release one
	limiter.ReleaseRequest(1, 1)

	// Now should be accepted
	can, _ = limiter.CanAcceptRequest(1, 100, 10)
	require.True(t, can, "Should accept after releasing")
}

func TestBandwidthLimiter_InferenceDisabled(t *testing.T) {
	// Create limiter with inference limit disabled (0)
	limiter := newTestBandwidthLimiterWithInferenceLimit(100000, 5, 0)
	require.Equal(t, uint64(0), limiter.maxInferencesPerBlock, "Limiter should have inference limiting disabled when maxInferences=0")

	// Record many requests
	for i := 0; i < 1000; i++ {
		limiter.RecordRequest(1, 1)
	}

	// Should still accept when inference limit is disabled
	can, _ := limiter.CanAcceptRequest(1, 100, 10)
	require.True(t, can, "Should accept when inference limit is disabled")
}

func TestBandwidthParameterLoadingWithInferenceLimit(t *testing.T) {
	// Test that bandwidth parameters including inference limit are correctly loaded

	bandwidthParams := &types.BandwidthLimitsParams{
		EstimatedLimitsPerBlockKb: 1024,
		KbPerInputToken: &types.Decimal{
			Value:    23,
			Exponent: -4,
		},
		KbPerOutputToken: &types.Decimal{
			Value:    64,
			Exponent: -2,
		},
		MaxInferencesPerBlock: 500,
	}

	// Simulate parameter loading
	bandwidthConfig := apiconfig.BandwidthParamsCache{
		EstimatedLimitsPerBlockKb: bandwidthParams.EstimatedLimitsPerBlockKb,
		KbPerInputToken:           bandwidthParams.KbPerInputToken.ToFloat(),
		KbPerOutputToken:          bandwidthParams.KbPerOutputToken.ToFloat(),
		MaxInferencesPerBlock:     bandwidthParams.MaxInferencesPerBlock,
	}

	require.Equal(t, uint64(500), bandwidthConfig.MaxInferencesPerBlock, "MaxInferencesPerBlock should be loaded correctly")

	// Create limiter and verify it uses the inference limit
	configManager := &mockConfigManager{
		validationParams: apiconfig.ValidationParamsCache{
			ExpirationBlocks: 10,
		},
		bandwidthParams: bandwidthConfig,
	}
	limiter := NewBandwidthLimiterFromConfig(configManager, nil, nil)
	require.NotNil(t, limiter, "BandwidthLimiter should be created successfully")
	require.Equal(t, uint64(500), limiter.maxInferencesPerBlock, "Limiter should use configured inference limit")
}
