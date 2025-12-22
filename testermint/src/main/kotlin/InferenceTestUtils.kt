package com.productscience

import com.productscience.data.InferencePayload
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.tinylog.kotlin.Logger
import java.time.Instant
import java.util.concurrent.Executors

/**
 * Common utility functions for inference testing.
 */

/**
 * Run parallel inference requests and return the actual InferencePayload objects.
 * This is the enhanced version that returns full inference data instead of just statuses.
 */
fun runParallelInferencesWithResults(
    genesis: LocalInferencePair,
    count: Int,
    waitForBlocks: Int = 20,
    maxConcurrentRequests: Int = Runtime.getRuntime().availableProcessors(),
    models: List<String> = listOf(defaultModel),
    inferenceRequest: InferenceRequestPayload = inferenceRequestObject,  // Allow custom request
): List<InferencePayload> = runBlocking {
    val overallStartTime = System.currentTimeMillis()
    
    // Launch coroutines with async and collect the deferred results
    val limitedDispatcher = Executors.newFixedThreadPool(maxConcurrentRequests).asCoroutineDispatcher()
    
    val requestStartTime = System.currentTimeMillis()
    
    val requests = List(count) { i ->
        async(limitedDispatcher) {
            val requestStart = System.currentTimeMillis()
            try {
                Logger.info("Making inference request $i")
                System.nanoTime()
                // This works, because the Instant.now() resolution gives us 3 zeros at the end, so we know these will be unique
                val timestamp = Instant.now().toEpochNanos() + i
                val result = genesis.makeInferenceRequest(inferenceRequest.copy(model = models.random()).toJson(), timestamp = timestamp)
                Logger.info("Inference request $i completed in ${System.currentTimeMillis() - requestStart}ms")
                result
            } catch (e: Exception) {
                Logger.error(e, "Error making inference request $i")
                null
            }
        }
    }

    // Wait for all requests to complete and collect their results
    val requestCollectionStart = System.currentTimeMillis()
    
    val results = requests.awaitAll()
    val requestCollectionEnd = System.currentTimeMillis()
    val requestPhaseTotal = requestCollectionEnd - requestStartTime
    
    val successfulRequests = results.filterNotNull()

    val blockWaitStart = System.currentTimeMillis()
    genesis.node.waitForNextBlock(waitForBlocks)
    val blockWaitEnd = System.currentTimeMillis()
    val blockWaitDuration = blockWaitEnd - blockWaitStart

    // Return actual inference objects
    val inferenceRetrievalStart = System.currentTimeMillis()
    
    val inferences = results.mapNotNull { result ->
        result?.let {
            try {
                val retrievalStart = System.currentTimeMillis()
                val inference = genesis.api.getInference(result.id)
                inference
            } catch (e: Exception) {
                null
            }
        }
    }
    
    val overallEndTime = System.currentTimeMillis()
    val totalDuration = overallEndTime - overallStartTime
    
    inferences
}

/**
 * Run parallel inference requests and return just the status codes.
 * This maintains backward compatibility with existing tests.
 */
fun runParallelInferences(
    genesis: LocalInferencePair,
    count: Int,
    waitForBlocks: Int = 20,
    maxConcurrentRequests: Int = Runtime.getRuntime().availableProcessors(),
    models: List<String> = listOf(defaultModel),
    inferenceRequest: InferenceRequestPayload = inferenceRequestObject,  // Allow custom request
): List<Int> {
    // Use the new function and extract statuses for backward compatibility
    val inferences = runParallelInferencesWithResults(genesis, count, waitForBlocks, maxConcurrentRequests, models, inferenceRequest)
    return inferences.map { it.status }
} 