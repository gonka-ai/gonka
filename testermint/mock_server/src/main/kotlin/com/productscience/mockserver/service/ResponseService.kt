package com.productscience.mockserver.service

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.model.ErrorResponse
import com.productscience.mockserver.model.OpenAIResponse
import com.productscience.mockserver.model.latestNonce
import org.slf4j.LoggerFactory
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicReference
import kotlin.collections.set

/**
 * Data class to represent either a successful response or an error response configuration.
 */
sealed class ResponseConfig {
    abstract val delay: Int
    abstract val streamDelay: Long

    data class Success(
        val responseBody: String,
        override val delay: Int,
        override val streamDelay: Long
    ) : ResponseConfig()

    data class Error(
        val errorResponse: ErrorResponse,
        override val delay: Int,
        override val streamDelay: Long
    ) : ResponseConfig()
}

@JvmInline
value class ScenarioName(val name: String)

@JvmInline
value class Endpoint(val path: String)

@JvmInline
value class ModelName(val name: String)

/**
 * Service for managing and modifying responses for various endpoints.
 */
class ResponseService {
    private val objectMapper = ObjectMapper()
        .registerKotlinModule()
        .setPropertyNamingStrategy(com.fasterxml.jackson.databind.PropertyNamingStrategies.SNAKE_CASE)
    private val logger = LoggerFactory.getLogger(ResponseService::class.java)

    // Store for inference responses keyed strongly just like PoC: (Endpoint, ModelName?, HostName)
    private val inferenceResponses = ConcurrentHashMap<Triple<Endpoint, ModelName?, HostName?>, ResponseConfig>()

    // Store for POC responses
    private val pocResponses = ConcurrentHashMap<Pair<HostName?, ScenarioName>, Long>()

    // Store for the last inference request
    private val lastInferenceRequest = AtomicReference<String?>(null)

    // No string keys anymore; use strongly typed value classes for the key parts.

    fun clearOverrides() {
        inferenceResponses.clear()
        pocResponses.clear()
        lastInferenceRequest.set(null)
    }

    /**
     * Sets the response for the inference endpoint.
     *
     * @param response The response body as a string
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @param model Optional model name to filter requests by
     * @return The endpoint path where the response is set
     */
    fun setInferenceResponse(
        response: String,
        delay: Int = 0,
        streamDelay: Long = 0,
        segment: String = "",
        model: ModelName? = null,
        host: HostName? = null
    ): String {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segment1 = if (cleanedSegment != null) "/$cleanedSegment" else ""
        val endpoint = "$segment1/v1/chat/completions"
        val key = Triple(Endpoint(endpoint), model, host)
        inferenceResponses[key] = ResponseConfig.Success(response, delay, streamDelay)
        logger.debug("Stored response for host='${host?.name}', endpoint='$endpoint', model='$model'")
        logger.debug("Response preview: ${response.take(50)}...")
        return endpoint
    }

    /**
     * Sets an error response for the inference endpoint.
     *
     * @param statusCode The HTTP status code to return
     * @param errorMessage Optional custom error message
     * @param errorType Optional custom error type
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @return The endpoint path where the error response is set
     */
    fun setInferenceErrorResponse(
        statusCode: Int,
        errorMessage: String? = null,
        errorType: String? = null,
        delay: Int = 0,
        streamDelay: Long = 0,
        segment: String = "",
        host: HostName? = null
    ): String {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segment1 = if (cleanedSegment != null) "/$cleanedSegment" else ""
        val endpoint = "$segment1/v1/chat/completions"
        val errorResponse = ErrorResponse(statusCode, errorMessage, errorType)
        logger.debug("Stored error response for host='${host?.name}', endpoint='$endpoint'")
        // Store as generic (no model) error for this endpoint+host
        val key = Triple(Endpoint(endpoint), null, host)
        inferenceResponses[key] = ResponseConfig.Error(errorResponse, delay, streamDelay)
        return endpoint
    }

    // Backward-compatible overload (defaults to localhost)
    fun setInferenceErrorResponse(
        statusCode: Int,
        errorMessage: String? = null,
        errorType: String? = null,
        delay: Int = 0,
        streamDelay: Long = 0,
        segment: String = ""
    ): String = setInferenceErrorResponse(
        statusCode,
        errorMessage,
        errorType,
        delay,
        streamDelay,
        segment,
        HostName("localhost")
    )

    /**
     * Gets the response configuration for the inference endpoint.
     *
     * @param endpoint The endpoint path
     * @param model Optional model name to filter responses by
     * @param host Optional host to filter responses by
     * @return ResponseConfig object, or null if not found
     */
    fun getInferenceResponseConfig(endpoint: String, model: String? = null, host: HostName?): ResponseConfig? {
        logger.debug("Getting inference response for host='${host?.name}', endpoint='$endpoint', model='$model'")
        val endpointKey = Endpoint(endpoint)
        return inferenceResponses[Triple(endpointKey, model?.let { ModelName(it) }, host)] ?:
          inferenceResponses[Triple(endpointKey, null, host)] ?:
          inferenceResponses[Triple(endpointKey, model?.let { ModelName(it) }, null)] ?:
          inferenceResponses[Triple(endpointKey, null, null)]
    }

    /**
     * Sets the POC response with the specified weight.
     *
     * @param weight The number of nonces to generate
     * @param scenarioName The name of the scenario
     */
    fun setPocResponse(weight: Long, host: HostName?, scenarioName: ScenarioName = ScenarioName("ModelState")) {
        logger.info("Setting POC response weight for host: $host, scenario: $scenarioName, weight: $weight")
        pocResponses[host to scenarioName] = weight
    }

    /**
     * Gets the POC response weight for the specified scenario.
     *
     * @param scenarioName The name of the scenario
     * @return The weight, or null if not found
     */
    fun getPocResponseWeight(hostName: HostName, scenarioName: ScenarioName = ScenarioName("ModelState")): Long? {
        val weight = pocResponses[hostName to scenarioName] ?: pocResponses[null to scenarioName]
        logger.info("Found POC response weight for host: $hostName, scenario: $scenarioName, weight: $weight")
        return weight
    }

    /**
     * Generates a POC response body with the specified weight.
     *
     * @param weight The number of nonces to generate
     * @param publicKey The public key from the request
     * @param blockHash The block hash from the request
     * @param blockHeight The block height from the request
     * @return The generated POC response body as a string
     */
    fun generatePocResponseBody(
        weight: Long,
        publicKey: String,
        blockHash: String,
        blockHeight: Int,
        nodeNumber: Int,
    ): String {
        // Generate 'weight' number of nonces
        // nodeNumber makes sure nonces are unique in a multi-node setup
        val start = latestNonce.getAndAdd(weight)
        val end = start + weight
        val nonces = (start until end).toList()
        // Generate distribution values evenly spaced from 0.0 to 1.0
        val dist = (1..weight).map { it.toDouble() / weight }

        return """
            {
              "public_key": "$publicKey",
              "block_hash": "$blockHash",
              "block_height": $blockHeight,
              "node_id": $nodeNumber,
              "nonces": $nonces,
              "dist": $dist,
              "received_dist": $dist
            }
        """.trimIndent()
    }

    /**
     * Sets the last inference request.
     *
     * @param request The request body as a string
     */
    fun setLastInferenceRequest(request: String) {
        lastInferenceRequest.set(request)
    }

    /**
     * Gets the last inference request.
     *
     * @return The last inference request as a string, or null if no request has been made
     */
    fun getLastInferenceRequest(): String? {
        return lastInferenceRequest.get()
    }
}
