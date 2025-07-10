package com.productscience.mockserver.service

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.model.OpenAIResponse
import io.ktor.http.*
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicReference

/**
 * Service for managing and modifying responses for various endpoints.
 */
class ResponseService {
    private val objectMapper = ObjectMapper()
        .registerKotlinModule()
        .setPropertyNamingStrategy(com.fasterxml.jackson.databind.PropertyNamingStrategies.SNAKE_CASE)

    // Store for inference responses by endpoint path: response body, delay, stream_delay
    private val inferenceResponses = ConcurrentHashMap<String, Triple<String, Int, Long>>()

    // Store for POC responses
    private val pocResponses = ConcurrentHashMap<String, Long>()

    // Store for the last inference request
    private val lastInferenceRequest = AtomicReference<String?>(null)

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
        val endpoint = "$segment/v1/chat/completions"
        inferenceResponses[endpoint] = Triple(response, delay, streamDelay)
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
     * Gets the response for the inference endpoint.
     * 
     * @param endpoint The endpoint path
     * @return Triple of response body, delay, and stream delay, or null if not found
     */
    fun getInferenceResponse(endpoint: String): Triple<String, Int, Long>? {
        return inferenceResponses[endpoint]
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
