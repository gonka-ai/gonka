package internal

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBandwidthLimiter_CanAcceptRequest(t *testing.T) {
	limiter := NewBandwidthLimiter(100, 10, 0.0023, 0.64) // 100 KB limit, default coefficients

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
	limiter := NewBandwidthLimiter(100, 10, 0.0023, 0.64)

	// Record a large request that will create conflict
	limiter.RecordRequest(1, 950) // Records 950 KB at block 11

	// Check that a new request starting at block 5 is now rejected
	// This checks range [5:15] which includes block 11 with 950 KB
	// Average existing = 950/10 = 95 KB per block
	// Need new request > 5 KB per block = 50 KB total to exceed 100 KB limit
	can, _ := limiter.CanAcceptRequest(5, 1000, 100) // ~67 KB request = 6.7 KB per block
	require.False(t, can, "Should not accept a new request that would exceed average limit")

	// Release the first request
	limiter.ReleaseRequest(1, 950)

	// Check that the same request is now accepted
	can, _ = limiter.CanAcceptRequest(5, 1000, 100)
	require.True(t, can, "Should accept a new request after releasing the conflicting one")
}

func TestBandwidthLimiter_Concurrency(t *testing.T) {
	limiter := NewBandwidthLimiter(100, 10, 0.0023, 0.64) // Lower limit for clearer test
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
	limiter := NewBandwidthLimiter(100, 5, 0.0023, 0.64)
	limiter.cleanupInterval = 10 * time.Millisecond // Speed up cleanup for test

	// Record usage - these will be recorded at completion blocks (start + 5)
	limiter.RecordRequest(1, 50) // Records at block 6
	limiter.RecordRequest(2, 50) // Records at block 7

	// Record usage on a much later block
	limiter.RecordRequest(20, 50) // Records at block 25

	// Wait for cleanup to run
	time.Sleep(20 * time.Millisecond)

	limiter.mu.RLock()
	defer limiter.mu.RUnlock()

	_, exists6 := limiter.usagePerBlock[6]   // From RecordRequest(1, 50)
	_, exists7 := limiter.usagePerBlock[7]   // From RecordRequest(2, 50)
	_, exists25 := limiter.usagePerBlock[25] // From RecordRequest(20, 50)

	require.False(t, exists6, "Usage for block 6 should have been cleaned up")
	require.False(t, exists7, "Usage for block 7 should have been cleaned up")
	require.True(t, exists25, "Usage for block 25 should not have been cleaned up")
}
