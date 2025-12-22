import com.productscience.*
import com.productscience.data.MsgFinishInference
import com.productscience.data.MsgStartInference
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.time.Duration
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlin.random.Random
import kotlin.test.assertNotNull
import com.productscience.assertions.assertThat
/**
 * Comprehensive end-to-end test for dynamic pricing algorithm.
 *
 * Tests the complete dynamic pricing cycle:
 * 1. Initial state: price at minimum (1000)
 * 2. Load generation: realistic utilization with controlled growth (75 regular inferences × 85 tokens ≈ 5.3x utilization)
 * 3. Time decay: price decreases after utilization drops (with 2% growth caps)
 */
@Timeout(value = 15, unit = TimeUnit.MINUTES)
class DynamicPricingTest : TestermintTest() {

    @Test
    fun `test dynamic pricing full cycle - load increase and decrease`() {
        logSection("=== STARTING DYNAMIC PRICING FULL CYCLE TEST ===")
        logSection("DPTEST: Test initialization starting")

        val (cluster, genesis) = initCluster(reboot = true)
        genesis.markNeedsReboot()
        logSection("DPTEST: Cluster initialized and waiting for CLAIM_REWARDS stage completed")

        // Setup mock responses for faster testing  
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(1))
        }
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        logSection("DPTEST: === PHASE 1: INITIAL STATE VERIFICATION ===")

        // Check initial price (should be MinPerTokenPrice = 1000)
        val initialPrice = getCurrentModelPrice(genesis, defaultModel)
        logSection("DPTEST: INITIAL_PRICE - model=$defaultModel, price=$initialPrice")
        assertTrue(initialPrice == 1000L, "Expected initial price 1000, got $initialPrice")

        logSection("DPTEST: === PHASE 2: LOAD GENERATION & PRICE INCREASE ===")

        // Measure actual blocks passed during load generation for accurate price calculation
        val startBlock = genesis.getCurrentBlockHeight()
        logSection("DPTEST: BLOCK_START - Starting load generation at block $startBlock")

        // Generate high load to trigger price increase using regular inference requests
        // Strategy: Single batch of 20 regular inferences for high utilization testing
        // Regular requests generate ~85 tokens each (20 × 85 = 1,700 tokens vs 1,800 capacity = 94% utilization)
        val loadGenerationStart = System.currentTimeMillis()
        logSection("DPTEST: LOAD_START - Generating 20 regular parallel inferences for 94% utilization")

        val allLoadResults = runParallelInferencesWithResults(
            genesis = genesis,
            count = 20,  // 20 inferences for 94% utilization (high overload)
            waitForBlocks = 4,  // Optimized from performance test
            maxConcurrentRequests = 200,  // Proven working configuration
            inferenceRequest = inferenceRequestObject  // Back to regular size requests
        )

        val loadGenerationEnd = System.currentTimeMillis()
        logSection("DPTEST: LOAD_COMPLETE - Generated ${allLoadResults.size}/20 regular inferences in ${loadGenerationEnd - loadGenerationStart}ms")

        val successfulLoadResults = allLoadResults.filter { it.actualCost != null }
        logSection("DPTEST: LOAD_SUCCESS - ${successfulLoadResults.size} successful inferences")

        // Log details about the load test results
        val totalLoadTokens = calculateTotalTokens(allLoadResults)
        logSection("DPTEST: LOAD_STATS - successful=${successfulLoadResults.size}, total_tokens=$totalLoadTokens")

        // Wait for pricing algorithm to process the load
        logSection("DPTEST: PRICING_WAIT_START - Waiting 20 seconds for pricing algorithm")
        Thread.sleep(Duration.ofSeconds(20))

        // Measure final block height to calculate actual blocks passed
        val endBlock = genesis.getCurrentBlockHeight()
        val actualBlocksPassed = endBlock - startBlock
        logSection("DPTEST: BLOCK_END - Pricing check at block $endBlock, actual_blocks_passed=$actualBlocksPassed")

        // Check price after high load
        val priceAfterLoad = getCurrentModelPrice(genesis, defaultModel)
        logSection("DPTEST: PRICE_AFTER_LOAD - price=$priceAfterLoad, initial_price=$initialPrice, increase=${priceAfterLoad - initialPrice}")

        // Verify price increased due to high utilization
        if (priceAfterLoad > 1000L) {
            logSection("DPTEST: PRICE_INCREASE_SUCCESS - price increased from $initialPrice to $priceAfterLoad")
        } else {
            logSection("DPTEST: PRICE_INCREASE_FAILED - price did not increase: $priceAfterLoad")
        }

        assertThat(priceAfterLoad).isGreaterThan(1000L)
            .`as`("Price should increase above 1000 due to high utilization")

        // Calculate expected utilization and price increase using actual blocks and network parameters
        val expectedPriceRange = calculateExpectedPriceRange(genesis, totalLoadTokens, actualBlocksPassed)
        logSection("DPTEST: UTILIZATION_CALC - total_tokens=$totalLoadTokens, blocks_passed=$actualBlocksPassed, expected_range=${expectedPriceRange.first}-${expectedPriceRange.second}")

        // Verify price follows elasticity formula (approximate)
        val priceInRange = priceAfterLoad >= expectedPriceRange.first && priceAfterLoad <= expectedPriceRange.second
        logSection("DPTEST: ELASTICITY_CHECK - price_in_range=$priceInRange, actual=$priceAfterLoad, range=${expectedPriceRange.first}-${expectedPriceRange.second}")

        assertThat(priceAfterLoad).isBetween(expectedPriceRange.first, expectedPriceRange.second)
            .`as`("Price should follow elasticity formula")

        logSection("DPTEST: PHASE2_COMPLETE - Load generation and price increase verified")

        // PHASE 3: Utilization Window Reset & Price Decrease
        logSection("=== PHASE 3: Utilization Window Reset & Price Decrease ===")
        logSection("DPTEST: PHASE3_START - Beginning price decrease verification")

        val waitStartTime = System.currentTimeMillis()
        logSection("DPTEST: WAIT_START - Waiting 70 seconds for utilization window reset (60s window + 10s buffer), start_time=$waitStartTime")
        Thread.sleep(Duration.ofSeconds(70)) // UtilizationWindowDuration (60s) + buffer
        val waitEndTime = System.currentTimeMillis()
        logSection("DPTEST: WAIT_COMPLETE - Wait finished, duration=${(waitEndTime - waitStartTime) / 1000}s")

        // Check price after utilization window reset
        val priceAfterWait = getCurrentModelPrice(genesis, defaultModel)
        logSection("DPTEST: PRICE_AFTER_WAIT - price=$priceAfterWait, price_after_load=$priceAfterLoad, change=${priceAfterWait - priceAfterLoad}")

        // Verify price started decreasing (should be less than peak or moving toward 1000)
        val priceDecreasing = priceAfterWait < priceAfterLoad
        val priceAtMinimum = priceAfterWait == 1000L
        logSection("DPTEST: PRICE_BEHAVIOR - decreasing=$priceDecreasing, at_minimum=$priceAtMinimum")

        assertThat(priceAfterWait).satisfiesAnyOf(
            { price -> assertThat(price).isLessThan(priceAfterLoad) }, // Decreasing
            { price -> assertThat(price).isEqualTo(1000L) }             // Back to minimum
        ).`as`("Price should decrease after utilization window reset")

        // Verify price floor enforcement
        val priceAboveFloor = priceAfterWait >= 1000L
        logSection("DPTEST: PRICE_FLOOR_CHECK - above_floor=$priceAboveFloor, price=$priceAfterWait, floor=1000")

        assertThat(priceAfterWait).isGreaterThanOrEqualTo(1000L)
            .`as`("Price should never go below MinPerTokenPrice (1000)")

        logSection("DPTEST: PHASE3_COMPLETE - Price decrease and floor verification passed")
        logSection("DPTEST: TEST_SUCCESS - Dynamic pricing cycle completed successfully")
        logSection("=== DYNAMIC PRICING CYCLE TEST COMPLETED SUCCESSFULLY ===")
    }

    @Test
    fun `test dynamic pricing for StartInference or FinishInference only`() {
        val (cluster, genesis) = initCluster(reboot = true)
        genesis.markNeedsReboot()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Run up the price with lots of inferences")
        val initialPrice = getCurrentModelPrice(genesis, defaultModel)
        assertThat(initialPrice).isEqualTo(1000L)
        val startBlock = genesis.getCurrentBlockHeight()
        val allLoadResults = runParallelInferencesWithResults(
            genesis = genesis,
            count = 20,  // 20 inferences for 94% utilization (high overload)
            waitForBlocks = 4,  // Optimized from performance test
            maxConcurrentRequests = 200,  // Proven working configuration
            inferenceRequest = inferenceRequestObject,  // Back to regular size requests,
            models = listOf(defaultModel)
        )
        logSection("Waiting for price increase")
        Thread.sleep(Duration.ofSeconds(20))
        val endBlock = genesis.getCurrentBlockHeight()
        val actualBlocksPassed = endBlock - startBlock
        val newPrice = getCurrentModelPrice(genesis, defaultModel)
        assertThat(newPrice).isGreaterThan(1000L)
        logSection("Submit raw StartInference")
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
        // Phase 3: TA signs hash manually for raw transaction
        val requestHash = sha256(inferenceRequest)
        val taSignature =
            genesis.node.signPayload(requestHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = requestHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = defaultModel,
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            originalPromptHash = requestHash
        )

        val response = genesis.submitMessage(message)
        assertThat(response).isSuccess()
        assertThat(response.code).isZero()
        println(response)
        val inference = genesis.node.getInference(signature)
        assertNotNull(inference)
        assertThat(inference.inference.perTokenPrice).isGreaterThan(1000L)
        logSection("Testing FinishInference with no StartInference")

        val finishTimestamp = Instant.now().toEpochNanos()
        // Phase 3: Dev signs original_prompt_hash, TA/Executor sign prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash // Same when no seed modification
        val finishSignature =
            genesis.node.signPayload(originalPromptHash + finishTimestamp.toString() + genesisAddress, null)
        val finishTaSignature =
            genesis.node.signPayload(
                promptHash + finishTimestamp.toString() + genesisAddress + genesisAddress,
                null
            )
        val finishMessage = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = finishSignature,
            promptTokenCount = 10,
            requestTimestamp = finishTimestamp,
            transferSignature = finishTaSignature,
            responseHash = "fjdsf",
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = finishTaSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            // originalPrompt removed - Phase 6: payloads stored offchain
            model = defaultModel,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash
        )
        val finishResponse = genesis.submitMessage(finishMessage)
        assertThat(finishResponse).isSuccess()
        val finishInference = genesis.node.getInference(finishSignature)
        assertNotNull(finishInference)
        assertThat(finishInference.inference.perTokenPrice).isGreaterThan(1000L)


    }

    @Test
    @Tag("exclude")
    fun `test single batch performance - 100 regular parallel inferences`() = runBlocking {
        logSection("PERFTEST: Starting performance test with 100 regular parallel inferences")

        // Initialize cluster and wait for readiness
        val (cluster, genesis) = initCluster(reboot = true)

        // Setup mock responses for faster testing  
        cluster.allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(1))
        }
        logSection("PERFTEST: Mock responses configured for ${cluster.allPairs.size} pairs")

        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("PERFTEST: Cluster ready, starting batch inference test")

        // Test single batch of 100 regular parallel inferences (each generates ~85 tokens for 700% utilization)
        val startTime = System.currentTimeMillis()
        logSection("PERFTEST: BATCH_START - Starting 100 regular parallel inferences at timestamp=$startTime")

        val inferences = runParallelInferencesWithResults(
            genesis = genesis,
            count = 100,  // 100 inferences for 85% utilization testing
            waitForBlocks = 4,  // Optimized from previous testing
            maxConcurrentRequests = 200,  // Proven working configuration
            inferenceRequest = inferenceRequestObject  // Regular size requests
        )

        // Record end time
        val endTime = System.currentTimeMillis()
        val totalDuration = endTime - startTime

        logSection("PERFTEST: BATCH_END - Completed at timestamp=$endTime")
        logSection("PERFTEST: DURATION - Total time: ${totalDuration}ms (${totalDuration / 1000.0}s)")
        logSection("PERFTEST: RESULTS - requested=100, completed=${inferences.size}, successful=${inferences.size}")

        // Calculate token statistics
        val totalTokens = inferences.sumOf {
            (it.promptTokenCount?.toLong() ?: 0L) + (it.completionTokenCount?.toLong() ?: 0L)
        }
        val avgTokensPerInference = if (inferences.isNotEmpty()) totalTokens / inferences.size else 0

        logSection("PERFTEST: TOKENS - total_tokens=$totalTokens, avg_per_inference=$avgTokensPerInference")
        logSection("PERFTEST: SUCCESS_RATE - ${inferences.size}/100 = ${(inferences.size * 100) / 100}%")

        logSection("=== PERFORMANCE TEST COMPLETED ===")
    }

    private fun getCurrentModelPrice(genesis: com.productscience.LocalInferencePair, modelId: String): Long {
        logSection("DPTEST: PRICE_QUERY_START - querying price for model=$modelId")
        try {
            // Query current price for the model using ApplicationCLI method
            val response = genesis.node.getModelPerTokenPrice(modelId)
            logSection("DPTEST: PRICE_QUERY_RESPONSE - found=${response.found}, price_string='${response.price}'")

            return if (response.found) {
                val price = response.price.toLongOrNull() ?: 1000L
                logSection("DPTEST: PRICE_QUERY_SUCCESS - parsed_price=$price")
                price
            } else {
                logSection("DPTEST: PRICE_QUERY_NOT_FOUND - using default price=1000")
                1000L // Default to MinPerTokenPrice if not found
            }
        } catch (e: Exception) {
            logSection("DPTEST: PRICE_QUERY_ERROR - Failed to query model price: ${e.message}")
            return 1000L // Default to MinPerTokenPrice if query fails
        }
    }

    private fun runSingleInference(genesis: com.productscience.LocalInferencePair): com.productscience.data.InferencePayload {
        logSection("DPTEST: SINGLE_INF_START - Starting single inference")
        val seed = Random.nextInt()
        logSection("DPTEST: SINGLE_INF_SEED - generated seed=$seed")

        val response = genesis.makeInferenceRequest(
            inferenceRequestObject.copy(
                maxCompletionTokens = 100,
                seed = seed
            ).toJson()
        )
        logSection("DPTEST: SINGLE_INF_REQUESTED - inference_id=${response.id}")

        // Wait for completion
        var inference: com.productscience.data.InferencePayload? = null
        var attempts = 0
        while (inference?.actualCost == null && attempts < 10) {
            Thread.sleep(Duration.ofSeconds(1))
            attempts++
            try {
                inference = genesis.api.getInference(response.id)
                logSection("DPTEST: SINGLE_INF_POLL - attempt=$attempts, status=${inference?.status}, cost=${inference?.actualCost}")
            } catch (e: Exception) {
                logSection("DPTEST: SINGLE_INF_POLL_ERROR - attempt=$attempts, error=${e.message}")
                // Continue waiting
            }
        }

        checkNotNull(inference) { "Single inference did not complete" }
        checkNotNull(inference.actualCost) { "Single inference cost not calculated" }

        logSection("DPTEST: SINGLE_INF_COMPLETE - final_cost=${inference.actualCost}, prompt_tokens=${inference.promptTokenCount}, completion_tokens=${inference.completionTokenCount}")

        return inference
    }

    private fun calculateTotalTokens(inferences: List<com.productscience.data.InferencePayload>): Long {
        return inferences.sumOf { inference ->
            (inference.promptTokenCount?.toLong() ?: 0L) + (inference.completionTokenCount?.toLong() ?: 0L)
        }
    }

    private fun calculateExpectedPriceRange(
        genesis: com.productscience.LocalInferencePair,
        totalTokens: Long,
        actualBlocksPassed: Long
    ): Pair<Long, Long> {
        logSection("DPTEST: PRICE_CALC_START - calculating expected price range for total_tokens=$totalTokens, blocks_passed=$actualBlocksPassed")

        // Get network parameters instead of using hardcoded values
        val params = genesis.getParams()
        val dynamicPricingParams = params.dynamicPricingParams

        if (dynamicPricingParams == null) {
            logSection("DPTEST: NO_DYNAMIC_PRICING - dynamicPricingParams is null, using fallback range")
            return Pair(1000L, 1200L)  // Fallback range if params not available
        }

        // Extract network parameters
        val stabilityLowerBound = dynamicPricingParams.stabilityZoneLowerBound.toDouble()
        val stabilityUpperBound = dynamicPricingParams.stabilityZoneUpperBound.toDouble()
        val priceElasticity = dynamicPricingParams.priceElasticity.toDouble()
        val minPrice = dynamicPricingParams.minPerTokenPrice
        val utilizationWindowDuration = dynamicPricingParams.utilizationWindowDuration

        logSection("DPTEST: NETWORK_PARAMS - stability_zone=($stabilityLowerBound-$stabilityUpperBound), price_elasticity=$priceElasticity, min_price=$minPrice, window_duration=${utilizationWindowDuration}s")

        // Calculate utilization based on network capacity
        // Based on regular requests: ~85 tokens per inference average
        // With 20 regular inferences: ~1,700 tokens expected (94% utilization vs 1,800 capacity)
        // Utilization = tokens_processed_in_window / capacity
        val capacity = 1800L  // 30 tokens/sec × 60 seconds for 3-node test cluster
        val estimatedUtilization = totalTokens.toDouble() / capacity

        logSection("DPTEST: UTILIZATION_EST - capacity=$capacity, utilization=$estimatedUtilization, stability_zone=($stabilityLowerBound-$stabilityUpperBound)")

        if (estimatedUtilization > stabilityUpperBound) {
            logSection("DPTEST: HIGH_UTILIZATION - utilization=$estimatedUtilization > $stabilityUpperBound%, calculating price increase")

            // Calculate max price multiplier using the actual dynamic pricing formula:
            // maxIncreasePerBlock = 1.0 + (maxExcessDeviation * elasticity)  
            // Use maxExcessDeviation specifically for price increases (high utilization scenario)
            val maxExcessDeviation = 1.0 - stabilityUpperBound  // e.g., 1.0 - 0.60 = 0.40
            val maxIncreasePerBlock = 1.0 + (maxExcessDeviation * priceElasticity)

            // Proportional per-block factor (spec: linear w.r.t. deviation)
            val utilizationExcess = estimatedUtilization - stabilityUpperBound
            var perBlockFactor = 1.0 + (utilizationExcess * priceElasticity)
            if (perBlockFactor > maxIncreasePerBlock) {
                perBlockFactor = maxIncreasePerBlock
            }

            logSection("DPTEST: ELASTICITY_CALC - stability_bounds=($stabilityLowerBound-$stabilityUpperBound), excess=$utilizationExcess, per_block_factor=$perBlockFactor, max_increase_per_block=$maxIncreasePerBlock (${((maxIncreasePerBlock - 1.0) * 100).toInt()}%)")

            // Apply -3/+1 block buffer for realistic timing variations
            // -3: Initial blocks may not reflect full load yet
            // +1: Tests rarely run much longer than expected
            val minBlocks = maxOf(1, actualBlocksPassed - 3)
            val maxBlocks = actualBlocksPassed + 1

            logSection("DPTEST: BLOCK_BUFFER - actual_blocks=$actualBlocksPassed, range=$minBlocks-$maxBlocks")

            // Compound with integer truncation each block and enforce min price floor
            // Previously we just used Math.pow, resulting in higher expectations than reality
            fun compoundWithFloor(startPrice: Long, blocks: Int): Long {
                var p = startPrice.toDouble()
                repeat(blocks) {
                    p = kotlin.math.floor(p * perBlockFactor)
                }
                return maxOf(minPrice.toLong(), p.toLong())
            }

            val startPrice = minPrice.toLong() // assuming base/current == min for tests
            val minPriceIncrease = compoundWithFloor(startPrice, minBlocks.toInt())
            val maxPriceIncrease = compoundWithFloor(startPrice, maxBlocks.toInt())

            logSection("DPTEST: PRICE_PROJECTION - per_block=$perBlockFactor, min_blocks=$minBlocks->$minPriceIncrease, max_blocks=$maxBlocks->$maxPriceIncrease")

            return Pair(minPriceIncrease, maxPriceIncrease)
        } else {
            logSection("DPTEST: NORMAL_UTILIZATION - utilization=$estimatedUtilization <= $stabilityUpperBound%, price should stay stable")
            // Within or below stability zone - price should remain at base level
            return Pair(minPrice, minPrice)
        }
    }
}