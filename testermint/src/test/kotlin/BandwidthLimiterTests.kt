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
        val (cluster, genesis) = initCluster(reboot = true)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        logSection("=== Testing Bandwidth Limiter ===")

        // Step 1: Send two parallel large requests to test bandwidth limiting
        logSection("1. Sending two parallel large requests to exceed bandwidth limit")
        val genesisAddress = genesis.node.getAddress()
        
        // Create two large requests that together exceed the 1024KB limit
        // Each request: ~600KB, total ~1200KB > 1024KB limit
        val timestamp1 = Instant.now().toEpochNanos()
        val timestamp2 = timestamp1 + 1 // Ensure unique timestamp
        
        val largeRequest1 = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "First large request for bandwidth testing.")),
            maxTokens = 900 // ~576KB (900 * 0.64)
        )
        val largeRequest2 = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Second large request for bandwidth testing.")),
            maxTokens = 900 // ~576KB (900 * 0.64)
        )
        
        val request1Json = cosmosJson.toJson(largeRequest1)
        val request2Json = cosmosJson.toJson(largeRequest2)
        
        val signature1 = genesis.node.signPayload(request1Json, null, timestamp1, genesisAddress)
        val signature2 = genesis.node.signPayload(request2Json, null, timestamp2, genesisAddress)

        // Send both requests in parallel using threads
        logSection("2. Sending parallel requests - one should succeed, one should fail with 429")
        
        var response1: OpenAIResponse? = null
        var response2: OpenAIResponse? = null
        var exception1: Exception? = null
        var exception2: Exception? = null
        
        val thread1 = Thread {
            try {
                response1 = genesis.api.makeInferenceRequest(request1Json, genesisAddress, signature1, timestamp1)
            } catch (e: Exception) {
                exception1 = e
            }
        }
        
        val thread2 = Thread {
            try {
                response2 = genesis.api.makeInferenceRequest(request2Json, genesisAddress, signature2, timestamp2)
            } catch (e: Exception) {
                exception2 = e
            }
        }
        
        // Start both threads simultaneously
        thread1.start()
        thread2.start()
        
        // Wait for both to complete
        thread1.join()
        thread2.join()
        
        // One should succeed, one should fail with 429
        val successCount = listOf(response1, response2).count { it != null }
        val failureCount = listOf(exception1, exception2).count { 
            it is FuelError && it.message?.contains("429") == true 
        }
        
        logSection("3. Verifying results: $successCount successes, $failureCount 429 failures")
        assertThat(successCount).isEqualTo(1)
        assertThat(failureCount).isEqualTo(1)
        logSection("✓ Bandwidth limiter correctly allowed one request and rejected one with 429")

        // Step 4: Wait for requests to complete and test sequential request (should succeed)
        logSection("4. Waiting for requests to complete, then sending another large request")
        genesis.node.waitForNextBlock(5) // Wait for completion and bandwidth release
        
        val timestamp3 = Instant.now().toEpochNanos() + 10 // New timestamp
        val largeRequest3 = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Third request after bandwidth release.")),
            maxTokens = 900 // ~576KB
        )
        val request3Json = cosmosJson.toJson(largeRequest3)
        val signature3 = genesis.node.signPayload(request3Json, null, timestamp3, genesisAddress)
        
        // This should succeed now that previous requests have completed
        val response3 = genesis.api.makeInferenceRequest(request3Json, genesisAddress, signature3, timestamp3)
        assertThat(response3.id).isEqualTo(signature3)
        assertNotNull(response3)
        logSection("✓ Sequential large request succeeded after bandwidth release")

        logSection("=== Bandwidth Limiter Test Completed Successfully ===")
    }
}

