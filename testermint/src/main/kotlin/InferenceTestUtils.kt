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
): List<InferencePayload> = runBlocking {
    // Launch coroutines with async and collect the deferred results

    val limitedDispatcher = Executors.newFixedThreadPool(maxConcurrentRequests).asCoroutineDispatcher()
    val requests = List(count) { i ->
        async(limitedDispatcher) {
            Logger.warn("Starting request $i")
            try {
                System.nanoTime()
                // This works, because the Instant.now() resolution gives us 3 zeros at the end, so we know these will be unique
                val timestamp = Instant.now().toEpochNanos() + i
                val result = genesis.makeInferenceRequest(inferenceRequestObject.copy(model = models.random()).toJson(), timestamp = timestamp)
                Logger.info("Result for $i: $result\n\n\n")
                result
            } catch (e: Exception) {
                Logger.error("Error making inference request: ${e.message}")
                null
            } finally {
                Logger.warn("Finished request $i")
            }
        }
    }

    // Wait for all requests to complete and collect their results
    val results = requests.map { it.await() }

    genesis.node.waitForNextBlock(waitForBlocks)

    // Return actual inference objects
    results.mapNotNull { result ->
        result?.let {
            genesis.api.getInference(result.id)
        }
    }
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
): List<Int> {
    // Use the new function and extract statuses for backward compatibility
    val inferences = runParallelInferencesWithResults(genesis, count, waitForBlocks, maxConcurrentRequests, models)
    return inferences.map { it.status }
} 