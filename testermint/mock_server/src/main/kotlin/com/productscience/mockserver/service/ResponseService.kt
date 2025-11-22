package com.productscience.mockserver.service

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.model.OpenAIResponse
import com.productscience.mockserver.model.ErrorResponse
import io.ktor.http.*
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicReference

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

/**
 * Service for managing and modifying responses for various endpoints.
 */
class ResponseService {
    private val objectMapper = ObjectMapper()
        .registerKotlinModule()
        .setPropertyNamingStrategy(com.fasterxml.jackson.databind.PropertyNamingStrategies.SNAKE_CASE)

    // Store for inference responses by endpoint path and model
    private val inferenceResponses = ConcurrentHashMap<String, ResponseConfig>()

    // Store for POC responses
    private val pocResponses = ConcurrentHashMap<String, Long>()

    // Store for the last inference request
    private val lastInferenceRequest = AtomicReference<String?>(null)

    /**
     * Creates a key for storing responses, combining endpoint and model.
     */
    private fun createResponseKey(endpoint: String, model: String?): String {
        return if (model != null) "$endpoint::$model" else endpoint
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
    fun setInferenceResponse(response: String, delay: Int = 0, streamDelay: Long = 0, segment: String = "", model: String? = null): String {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segment1 = if (cleanedSegment != null) "/$cleanedSegment" else ""
        val endpoint = "$segment1/v1/chat/completions"
        val key = createResponseKey(endpoint, model)
        inferenceResponses[key] = ResponseConfig.Success(response, delay, streamDelay)
        println("DEBUG: Stored response for endpoint='$endpoint', model='$model', key='$key'")
        println("DEBUG: Response preview: ${response.take(50)}...")
        println("DEBUG: Current keys in store: ${inferenceResponses.keys}")
        return endpoint
    }

    /**
     * Sets the response for the inference endpoint using an OpenAIResponse object.
     * 
     * @param openAIResponse The OpenAIResponse object
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @param model Optional model name to filter requests by
     * @return The endpoint path where the response is set
     */
    fun setInferenceResponse(
        openAIResponse: OpenAIResponse,
        delay: Int = 0,
        streamDelay: Long = 0,
        segment: String = "",
        model: String? = null
    ): String {
        val response = objectMapper.writeValueAsString(openAIResponse)
        return setInferenceResponse(response, delay, streamDelay, segment, model)
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
        segment: String = ""
    ): String {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segment1 = if (cleanedSegment != null) "/$cleanedSegment" else ""
        val endpoint = "$segment1/v1/chat/completions"
        val errorResponse = ErrorResponse(statusCode, errorMessage, errorType)
        println("DEBUG: Storing error response for endpoint='$endpoint' with statusCode=$statusCode")
        inferenceResponses[endpoint] = ResponseConfig.Error(errorResponse, delay, streamDelay)
        return endpoint
    }

    /**
     * Gets the response configuration for the inference endpoint.
     * 
     * @param endpoint The endpoint path
     * @param model Optional model name to filter responses by
     * @return ResponseConfig object, or null if not found
     */
    fun getInferenceResponseConfig(endpoint: String, model: String? = null): ResponseConfig? {
        // First try to get model-specific response
        println("DEBUG: Getting inference response for endpoint='$endpoint', model='$model'")
        if (model != null) {
            val modelSpecificKey = createResponseKey(endpoint, model)
            println("DEBUG: Checking for model-specific response with key='$modelSpecificKey'")
            inferenceResponses.forEach {
                println("DEBUG: Available key: ${it.key}, Model: ${it.key.split("::").getOrNull(1)}")
            }
            val modelSpecificResponse = inferenceResponses[modelSpecificKey]
            if (modelSpecificResponse != null) {
                println("DEBUG: Found model-specific response for key='$modelSpecificKey'")
                return modelSpecificResponse
            }
        }

        // Fall back to generic response for the endpoint
        print("DEBUG: Checking for generic response for endpoint='$endpoint'")
        return inferenceResponses[endpoint]
    }

    /**
     * Gets the response for the inference endpoint (backward compatibility).
     *
     * @param endpoint The endpoint path
     * @param model Optional model name to filter responses by
     * @return Triple of response body, delay, and stream delay, or null if not found or if it's an error response
     */
    fun getInferenceResponse(endpoint: String, model: String? = null): Triple<String, Int, Long>? {
        return when (val config = getInferenceResponseConfig(endpoint, model)) {
            is ResponseConfig.Success -> Triple(config.responseBody, config.delay, config.streamDelay)
            else -> null
        }
    }

    /**
     * Sets the POC response with the specified weight.
     * 
     * @param weight The number of nonces to generate
     * @param scenarioName The name of the scenario
     */
    fun setPocResponse(weight: Long, scenarioName: String = "ModelState") {
        pocResponses[scenarioName] = weight
    }

    /**
     * Gets the POC response weight for the specified scenario.
     * 
     * @param scenarioName The name of the scenario
     * @return The weight, or null if not found
     */
    fun getPocResponseWeight(scenarioName: String = "ModelState"): Long? {
        return pocResponses[scenarioName]
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
        val start = (nodeNumber - 1) * weight + 1
        val end = nodeNumber * weight
        val nonces = (start..end).toList()
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
     * Generates a POC validation response body with the specified weight.
     * 
     * @param weight The number of nonces to generate
     * @param publicKey The public key from the request
     * @param blockHash The block hash from the request
     * @param blockHeight The block height from the request
     * @param rTarget The r_target from the request
     * @param fraudThreshold The fraud_threshold from the request
     * @return The generated POC validation response body as a string
     */
    fun generatePocValidationResponseBody(
        weight: Long,
        publicKey: String,
        blockHash: String,
        blockHeight: Int,
        rTarget: Double,
        fraudThreshold: Double
    ): String {
        // Generate 'weight' number of nonces
        val nonces = (1..weight).toList()
        // Generate distribution values evenly spaced from 0.0 to 1.0
        val dist = nonces.map { it.toDouble() / weight }

        return """
            {
              "public_key": "$publicKey",
              "block_hash": "$blockHash",
              "block_height": $blockHeight,
              "nonces": $nonces,
              "dist": $dist,
              "received_dist": $dist,
              "r_target": $rTarget,
              "fraud_threshold": $fraudThreshold,
              "n_invalid": 0,
              "probability_honest": 0.99,
              "fraud_detected": false
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
