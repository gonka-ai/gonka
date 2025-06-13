package com.productscience

import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.github.tomakehurst.wiremock.stubbing.StubMapping
import com.productscience.data.OpenAIResponse
import org.tinylog.kotlin.Logger

/**
 * Implementation of IInferenceMock that works with the Ktor-based mock server.
 * This class uses HTTP requests to interact with the mock server endpoints.
 */
class MockServerInferenceMock(private val baseUrl: String, val name: String) : IInferenceMock {

    /**
     * Sets the response for the inference endpoint.
     *
     * @param response The response body as a string
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @param model Optional model name to filter requests by
     * @return null (StubMapping is not used in this implementation)
     */
    override fun setInferenceResponse(response: String, delay: Int, streamDelay: Long, segment: String, model: String?): StubMapping? {
        val requestBody = """
            {
                "response": ${cosmosJson.toJson(response)},
                "delay": $delay,
                "stream_delay": $streamDelay,
                "segment": ${cosmosJson.toJson(segment)},
                "model": ${if (model != null) cosmosJson.toJson(model) else "null"}
            }
        """.trimIndent()

        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/responses/inference")
                .jsonBody(requestBody)
                .responseString()

            Logger.debug("Set inference response: $response")
        } catch (e: Exception) {
            Logger.error("Failed to set inference response: ${e.message}")
        }

        return null // StubMapping is not used in this implementation
    }

    /**
     * Sets the response for the inference endpoint using an OpenAIResponse object.
     *
     * @param openAIResponse The OpenAIResponse object
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @param model Optional model name to filter requests by
     * @return null (StubMapping is not used in this implementation)
     */
    override fun setInferenceResponse(
        openAIResponse: OpenAIResponse,
        delay: Int,
        streamDelay: Long,
        segment: String,
        model: String?
    ): StubMapping? = this.setInferenceResponse(openAiJson.toJson(openAIResponse), delay, streamDelay, segment, model)

    /**
     * Sets the POC response with the specified weight.
     *
     * @param weight The number of nonces to generate
     * @param scenarioName The name of the scenario
     */
    override fun setPocResponse(weight: Long, scenarioName: String) {
        val requestBody = """
            {
                "weight": $weight,
                "scenarioName": ${cosmosJson.toJson(scenarioName)}
            }
        """.trimIndent()

        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/responses/poc")
                .jsonBody(requestBody)
                .responseString()

            Logger.debug("Set POC response: $response")
        } catch (e: Exception) {
            Logger.error("Failed to set POC response: ${e.message}")
        }
    }

    /**
     * Sets the POC validation response with the specified weight.
     * Since the mock server uses the same weight for both POC and POC validation responses,
     * this method calls setPocResponse.
     *
     * @param weight The number of nonces to generate
     * @param scenarioName The name of the scenario
     */
    override fun setPocValidationResponse(weight: Long, scenarioName: String) {
        // The mock server uses the same weight for both POC and POC validation responses,
        // so we can just call setPocResponse
        setPocResponse(weight, scenarioName)
    }
}
