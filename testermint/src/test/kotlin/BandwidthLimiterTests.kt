import com.github.kittinunf.fuel.core.FuelError
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Instant
import kotlin.test.assertNotNull

class BandwidthLimiterTests : TestermintTest() {

    @Test
    fun `bandwidth limiter with rate limiting`() {
        // Initialize cluster with default configuration
        // The bandwidth limiter uses default limits from decentralized-api config:
        // - EstimatedLimitsPerBlockKb: 1024KB (default)
        // - KbPerInputToken: 0.0023 
        // - KbPerOutputToken: 0.64
        // - RequestLifespanBlocks: 10 (default)
        val (cluster, genesis) = initCluster(reboot = true)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        logSection("=== Testing Bandwidth Limiter ===")
        logSection("Bandwidth calculation: Total KB gets divided by requestLifespanBlocks (10)")
        logSection("Limit: 1024KB per block, so need >10240KB total to exceed limit")

        // Test: Use runParallelInferences with 20 requests to exceed bandwidth limit
        logSection("1. Sending 20 parallel requests using runParallelInferences")
        
        // Create requests that will collectively exceed bandwidth
        // Each request: maxTokens = 800 -> ~512KB total (800 * 0.64)
        // Per block: 512KB / 10 blocks = ~51.2KB per block per request
        // 20 requests: 20 * 51.2KB = ~1024KB per block (right at the limit)
        // Let's use 25 requests to definitely exceed: 25 * 51.2KB = ~1280KB per block > 1024KB limit
        val testRequest = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Bandwidth test request.")),
            maxTokens = 800 // ~512KB total, ~51.2KB per block
        )

        logSection("Each request: 800 maxTokens = ~512KB total = ~51.2KB per block")
        logSection("Testing with 20 requests = ~1024KB per block (should hit limit)")

        // Use runParallelInferences with many concurrent requests
        val results = runParallelInferencesWithResults(
            genesis = genesis,
            count = 20,
            waitForBlocks = 0, // Don't wait for completion initially
            maxConcurrentRequests = 20, // Allow all to run simultaneously
            inferenceRequest = testRequest
        )
        
        val successfulRequests = results.size
        val rejectedRequests = 20 - successfulRequests
        
        logSection("2. Results from runParallelInferences:")
        logSection("- Successful requests: $successfulRequests")
        logSection("- Rejected requests: $rejectedRequests")
        
        // Some requests should be rejected due to bandwidth limits
        assertThat(rejectedRequests).describedAs("Some requests should be rejected due to bandwidth limits").isGreaterThan(0)
        assertThat(successfulRequests).describedAs("Some requests should succeed").isGreaterThan(0)
        logSection("✓ Bandwidth limiter correctly rejected $rejectedRequests out of 20 requests")

        // Test with even more requests to ensure clear rejection
        logSection("3. Testing with even more requests (30) to ensure clear bandwidth limiting")
        
        val moreResults = runParallelInferencesWithResults(
            genesis = genesis,
            count = 30,
            waitForBlocks = 0,
            maxConcurrentRequests = 30,
            inferenceRequest = testRequest
        )
        
        val moreSuccessful = moreResults.size
        val moreRejected = 30 - moreSuccessful
        
        logSection("Results from 30 parallel requests:")
        logSection("- Successful requests: $moreSuccessful")
        logSection("- Rejected requests: $moreRejected")
        
        // With 30 requests, we should definitely see rejections
        assertThat(moreRejected).describedAs("More requests should be rejected with higher load").isGreaterThan(0)
        logSection("✓ Bandwidth limiter correctly rejected $moreRejected out of 30 requests")

        // Test bandwidth release
        logSection("4. Waiting for bandwidth release and testing again")
        genesis.node.waitForNextBlock(5) // Wait for completion and bandwidth release
        Thread.sleep(2000) // Additional wait for cleanup
        
        val finalResults = runParallelInferencesWithResults(
            genesis = genesis,
            count = 10,
            waitForBlocks = 0,
            maxConcurrentRequests = 10,
            inferenceRequest = testRequest
        )
        
        val finalSuccessful = finalResults.size
        logSection("After bandwidth release: $finalSuccessful out of 10 requests succeeded")
        
        // After release, more requests should succeed
        assertThat(finalSuccessful).describedAs("More requests should succeed after bandwidth release").isGreaterThan(5)
        logSection("✓ Bandwidth was released and new requests can be processed")

        logSection("=== Bandwidth Limiter Test Completed Successfully ===")
        logSection("Summary:")
        logSection("- First test (20 requests): $rejectedRequests rejected")
        logSection("- Second test (30 requests): $moreRejected rejected") 
        logSection("- After release (10 requests): ${10 - finalSuccessful} rejected")
        logSection("- Bandwidth limiter is working correctly!")
    }
}

