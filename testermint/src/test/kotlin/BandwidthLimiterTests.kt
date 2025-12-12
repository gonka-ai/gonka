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

        val bandWithSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::bandwidthLimitsParams] = spec<BandwidthLimitsParams> {
                        this[BandwidthLimitsParams::estimatedLimitsPerBlockKb] = 512L
                    }
                }
            }
        }

        val bandwidthConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(bandWithSpec) ?: bandWithSpec
        )

        // Initialize cluster with default configuration
        val (cluster, genesis) = initCluster(reboot = true, config = bandwidthConfig)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        logSection("=== Testing Bandwidth Limiter (21MB limit) ===")

        val testRequest = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Bandwidth test request.")),
            maxTokens = 800 // Large request: 800 * 0.64KB = ~512KB per request
        )

        logSection("1. Testing with single request (should succeed)")
        try {
            genesis.makeInferenceRequest(testRequest.toJson())
            logSection("✓ Single request succeeded")
        } catch (e: Exception) {
            logSection("✗ Single request failed: ${e.message}")
        }

        logSection("2. Testing bandwidth limiting with parallel requests")
        var successCount = 0
        var bandwidthRejectionCount = 0
        var otherErrorCount = 0
        
        val requests = (1..20).map { index ->
            Thread {
                try {
                    val uniqueTestRequest = inferenceRequestObject.copy(
                        messages = listOf(ChatMessage("user", "Bandwidth test request. {$index}")),
                        maxTokens = 800 // Large request: 800 * 0.64KB = ~512KB per request
                    )
                    genesis.makeInferenceRequest(uniqueTestRequest.toJson())
                    synchronized(this) {
                        successCount++
                        logSection("Request $index: SUCCESS")
                    }
                } catch (e: FuelError) {
                    val errorMessage = e.response.data.toString(Charsets.UTF_8)
                    synchronized(this) {
                        if (errorMessage.contains("Transfer Agent capacity reached") ||
                            errorMessage.contains("bandwidth") ||
                            e.response.statusCode == 429) {
                            bandwidthRejectionCount++
                            logSection("Request $index: BANDWIDTH REJECTED - $errorMessage")
                        } else {
                            otherErrorCount++
                            logSection("Request $index: OTHER ERROR - $errorMessage")
                        }
                    }
                } catch (e: Exception) {
                    synchronized(this) {
                        otherErrorCount++
                        logSection("Request $index: EXCEPTION - ${e.message}")
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
        
        val moreRequests = (1..30).map { index ->
            Thread {
                try {
                    val uniqueTestRequest = testRequest.copy(
                        messages = listOf(ChatMessage("user", "Bandwidth test request batch 2. {$index}"))
                    )
                    genesis.makeInferenceRequest(uniqueTestRequest.toJson())
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

        logSection("Results: $successCount successes, $bandwidthRejectionCount bandwidth rejections, $otherErrorCount other errors")

        // With 512KB limit and ~512KB requests, should get many rejections with 30 parallel requests
        assertThat(bandwidthRejectionCount).describedAs("Bandwidth limiter should reject many requests with 30 parallel requests (~15MB total vs 512KB limit)").isGreaterThan(10)
        logSection("✓ Bandwidth limiter correctly rejected $bandwidthRejectionCount out of 30 requests")

        // Test bandwidth release after waiting
        logSection("4. Waiting for bandwidth release and testing again")
        genesis.node.waitForNextBlock(10) // Wait longer for bandwidth to be released
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        var releasedSuccessCount = 0
        var releasedRejectionCount = 0
        repeat(10) { i ->
            try {
                genesis.makeInferenceRequest(testRequest.toJson())
                releasedSuccessCount++
                logSection("Post-release request ${i+1}: SUCCESS")
            } catch (e: FuelError) {
                val errorMessage = e.response.data.toString(Charsets.UTF_8)
                if (errorMessage.contains("Transfer Agent capacity reached") ||
                    errorMessage.contains("bandwidth") ||
                    e.response.statusCode == 429) {
                    releasedRejectionCount++
                    logSection("Post-release request ${i+1}: BANDWIDTH REJECTED")
                } else {
                    logSection("Post-release request ${i+1}: OTHER ERROR")
                }
            } catch (e: Exception) {
                logSection("Post-release request ${i+1}: EXCEPTION - ${e.message}")
            }
        }

        logSection("After bandwidth release: $releasedSuccessCount successes, $releasedRejectionCount rejections out of 10 requests")
        assertThat(releasedSuccessCount).describedAs("Some requests should succeed after bandwidth release").isGreaterThan(0)
        logSection("✓ Bandwidth was released and $releasedSuccessCount new requests succeeded")

        logSection("=== Bandwidth Limiter Test Completed Successfully ===")
    }

    @Test
    fun `inference count limiter rejects excess requests`() {
        val inferenceCountSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::bandwidthLimitsParams] = spec<BandwidthLimitsParams> {
                        this[BandwidthLimitsParams::estimatedLimitsPerBlockKb] = 100_000L // High KB limit
                        this[BandwidthLimitsParams::maxInferencesPerBlock] = 5L // Low inference limit
                    }
                }
            }
        }

        val inferenceCountConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(inferenceCountSpec) ?: inferenceCountSpec
        )

        val (cluster, genesis) = initCluster(reboot = true, config = inferenceCountConfig)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        logSection("=== Testing Inference Count Limiter (5 per block) ===")

        val testRequest = inferenceRequestObject.copy(
            messages = listOf(ChatMessage("user", "Inference count test.")),
            maxTokens = 10 // Small request to avoid bandwidth limit
        )

        logSection("1. Testing with single request (should succeed)")
        try {
            genesis.makeInferenceRequest(testRequest.toJson())
            logSection("Single request succeeded")
        } catch (e: Exception) {
            logSection("Single request failed: ${e.message}")
        }

        logSection("2. Testing inference count limiting with parallel requests")
        var successCount = 0
        var rejectionCount = 0
        var otherErrorCount = 0

        val requests = (1..20).map { index ->
            Thread {
                try {
                    val uniqueRequest = testRequest.copy(
                        messages = listOf(ChatMessage("user", "Inference count test {$index}"))
                    )
                    genesis.makeInferenceRequest(uniqueRequest.toJson())
                    synchronized(this) {
                        successCount++
                        logSection("Request $index: SUCCESS")
                    }
                } catch (e: FuelError) {
                    val errorMessage = e.response.data.toString(Charsets.UTF_8)
                    synchronized(this) {
                        if (errorMessage.contains("Transfer Agent capacity reached") ||
                            e.response.statusCode == 429) {
                            rejectionCount++
                            logSection("Request $index: REJECTED - $errorMessage")
                        } else {
                            otherErrorCount++
                            logSection("Request $index: OTHER ERROR - $errorMessage")
                        }
                    }
                } catch (e: Exception) {
                    synchronized(this) {
                        otherErrorCount++
                        logSection("Request $index: EXCEPTION - ${e.message}")
                    }
                }
            }
        }

        requests.forEach { it.start() }
        requests.forEach { it.join() }

        logSection("Results: $successCount successes, $rejectionCount rejections, $otherErrorCount other errors")

        // With 5 inferences per block limit and 20 parallel requests, should reject many
        assertThat(rejectionCount)
            .describedAs("Inference count limiter should reject requests exceeding 5 per block")
            .isGreaterThan(5)

        logSection("Inference count limiter correctly rejected $rejectionCount out of 20 requests")

        logSection("=== Inference Count Limiter Test Completed Successfully ===")
    }
}

