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
        val (cluster, genesis) = initCluster(reboot = true)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        logSection("=== Testing Bandwidth Limiter ===")
        
        // Create requests that will exceed bandwidth limits
        val testRequest = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Bandwidth test request.")),
            maxTokens = 800 // Large request to trigger bandwidth limits
        )

        logSection("1. Testing bandwidth limiter by sending parallel requests and counting errors")
        
        // Send requests in parallel and count both successes and bandwidth rejections
        var successCount = 0
        var bandwidthRejectionCount = 0
        var otherErrorCount = 0
        
        val requests = (1..20).map {
            Thread {
                try {
                    val response = genesis.makeInferenceRequest(testRequest.toJson())
                    synchronized(this) {
                        successCount++
                        logSection("Request $it: SUCCESS")
                    }
                } catch (e: FuelError) {
                    val errorMessage = e.response.data.toString(Charsets.UTF_8)
                    synchronized(this) {
                        if (errorMessage.contains("Transfer Agent capacity reached") || 
                            errorMessage.contains("bandwidth") ||
                            e.response.statusCode == 429) {
                            bandwidthRejectionCount++
                            logSection("Request $it: BANDWIDTH REJECTED - $errorMessage")
                        } else {
                            otherErrorCount++
                            logSection("Request $it: OTHER ERROR - $errorMessage")
                        }
                    }
                } catch (e: Exception) {
                    synchronized(this) {
                        otherErrorCount++
                        logSection("Request $it: EXCEPTION - ${e.message}")
                    }
                }
            }
        }
        
        // Start all requests simultaneously
        requests.forEach { it.start() }
        requests.forEach { it.join() }
        
        logSection("2. Results from 20 parallel requests:")
        logSection("- Successful requests: $successCount")
        logSection("- Bandwidth rejections: $bandwidthRejectionCount")
        logSection("- Other errors: $otherErrorCount")
        
        // Verify bandwidth limiter is working
        assertThat(bandwidthRejectionCount).describedAs("Bandwidth limiter should reject some requests").isGreaterThan(0)
        logSection("✓ Bandwidth limiter correctly rejected $bandwidthRejectionCount requests")

        // Test with even more requests to ensure consistent behavior
        logSection("3. Testing with more requests (30) to verify consistent bandwidth limiting")
        
        successCount = 0
        bandwidthRejectionCount = 0
        otherErrorCount = 0
        
        val moreRequests = (1..30).map {
            Thread {
                try {
                    val response = genesis.makeInferenceRequest(testRequest.toJson())
                    synchronized(this) { successCount++ }
                } catch (e: FuelError) {
                    val errorMessage = e.response.data.toString(Charsets.UTF_8)
                    synchronized(this) {
                        if (errorMessage.contains("Transfer Agent capacity reached") || 
                            errorMessage.contains("bandwidth") ||
                            e.response.statusCode == 429) {
                            bandwidthRejectionCount++
                        } else {
                            otherErrorCount++
                        }
                    }
                } catch (e: Exception) {
                    synchronized(this) { otherErrorCount++ }
                }
            }
        }
        
        moreRequests.forEach { it.start() }
        moreRequests.forEach { it.join() }
        
        logSection("Results from 30 parallel requests:")
        logSection("- Successful requests: $successCount")
        logSection("- Bandwidth rejections: $bandwidthRejectionCount")
        logSection("- Other errors: $otherErrorCount")
        
        assertThat(bandwidthRejectionCount).describedAs("More requests should be rejected with higher load").isGreaterThan(0)
        logSection("✓ Bandwidth limiter consistently rejected $bandwidthRejectionCount requests")

        // Test bandwidth release after waiting
        logSection("4. Waiting for bandwidth release and testing again")
        genesis.node.waitForNextBlock(5) // Wait for completion and bandwidth release
        Thread.sleep(2000) // Additional wait for cleanup
        
        successCount = 0
        bandwidthRejectionCount = 0
        
        val finalRequests = (1..10).map {
            Thread {
                try {
                    val response = genesis.makeInferenceRequest(testRequest.toJson())
                    synchronized(this) { successCount++ }
                } catch (e: FuelError) {
                    val errorMessage = e.response.data.toString(Charsets.UTF_8)
                    synchronized(this) {
                        if (errorMessage.contains("Transfer Agent capacity reached") || 
                            errorMessage.contains("bandwidth") ||
                            e.response.statusCode == 429) {
                            bandwidthRejectionCount++
                        }
                    }
                }
            }
        }
        
        finalRequests.forEach { it.start() }
        finalRequests.forEach { it.join() }
        
        logSection("After bandwidth release: $successCount successes, $bandwidthRejectionCount rejections")
        
        // After release, fewer requests should be rejected
        assertThat(successCount).describedAs("More requests should succeed after bandwidth release").isGreaterThan(3)
        logSection("✓ Bandwidth was released and new requests can be processed")

        logSection("=== Bandwidth Limiter Test Completed Successfully ===")
    }
}

