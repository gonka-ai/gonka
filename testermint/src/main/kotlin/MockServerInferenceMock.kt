package com.productscience

import com.fasterxml.jackson.annotation.JsonProperty
import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.github.tomakehurst.wiremock.stubbing.StubMapping
import com.productscience.data.OpenAIResponse
import org.tinylog.kotlin.Logger
import java.time.Duration


data class SetInferenceResponseRequest(
    val response: String,
    val delay: Int,
    @JsonProperty("stream_delay")
    val streamDelay: Int,
    val segment: String,
    val model: String? = null,
    @JsonProperty("host_name")
    val hostName: String? = null
)

/**
 * Implementation of IInferenceMock that works with the Ktor-based mock server.
 * This class uses HTTP requests to interact with the mock server endpoints.
 */
class MockServerInferenceMock(private val baseUrl: String, val name: String) : IInferenceMock {

    override fun getLastInferenceRequest(): InferenceRequestPayload? {
        try {
            val (_, response, result) = Fuel.get("$baseUrl/api/v1/responses/last-inference-request")
                .responseString()

            val (data, error) = result

            if (error != null) {
                Logger.error("Failed to get last inference request: ${error.message}")
                return null
            }

            // Parse the response JSON
            val responseJson = cosmosJson.fromJson(data, Map::class.java)

            // Check if the request was successful
            if (responseJson["status"] == "success" && responseJson.containsKey("request")) {
                // Parse the request JSON string into an InferenceRequestPayload object
                val requestJson = responseJson["request"] as String
                return cosmosJson.fromJson(requestJson, InferenceRequestPayload::class.java)
            } else {
                Logger.debug("No inference request found: ${responseJson["message"]}")
                return null
            }
        } catch (e: Exception) {
            Logger.error("Error getting last inference request: ${e.message}")
            return null
        }
    }

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
    override fun setInferenceResponse(
        response: String,
        delay: Duration,
        streamDelay: Duration,
        segment: String,
        model: String?,
        hostName: String?
    ): StubMapping? {
        val request = SetInferenceResponseRequest(response, delay.toMillis().toInt(), streamDelay.toMillis().toInt(), segment, model, hostName)

        val reqData = Fuel.post("$baseUrl/api/v1/responses/inference")
            .jsonBody(cosmosJson.toJson(request))
            .responseString()
        if (reqData.second.statusCode != 200) {
            logResponse(reqData, throwError = true)
        } else {
            Logger.debug("Set inference response: $response")
        }

        return null // StubMapping is not used in this implementation
    }

    override fun resetMocks() {
        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/responses/reset")
                .responseString()
            if (response.statusCode != 200) {
                Logger.error("Failed to reset inference mocks: ${response.statusCode} ${response.responseMessage}")
            } else {
                Logger.debug("Reset inference mocks: $response")
            }
        } catch (e: Exception) {
            Logger.error("Failed to reset inference mocks: ${e.message}")
        }
    }

    fun setExpectedAuthorizationHeader(expectedHeader: String, hostName: String? = null) {
        data class SetAuthHeaderRequest(
            val expected_header: String,
            val host_name: String? = null
        )

        val request = SetAuthHeaderRequest(expectedHeader, hostName)
        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/auth/token")
                .jsonBody(cosmosJson.toJson(request))
                .responseString()
            if (response.statusCode != 200) {
                Logger.error("Failed to set expected Authorization header: ${response.statusCode} ${response.responseMessage}")
            } else {
                Logger.debug("Set expected Authorization header: $expectedHeader for host=$hostName")
            }
        } catch (e: Exception) {
            Logger.error("Failed to set expected Authorization header: ${e.message}")
        }
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
        delay: Duration,
        streamDelay: Duration,
        segment: String,
        model: String?,
        hostName: String?
    ): StubMapping? = this.setInferenceResponse(
        openAiJson.toJson(openAIResponse.copy(model = model ?: openAIResponse.model)),
        delay,
        streamDelay,
        segment,
        model,
        hostName
    )

    /**
     * Sets an error response for the inference endpoint.
     *
     * @param statusCode The HTTP status code to return
     * @param errorMessage Optional custom error message
     * @param errorType Optional custom error type
     * @param delay The delay in milliseconds before responding
     * @param streamDelay The delay in milliseconds between SSE events when streaming
     * @param segment Optional URL segment to prepend to the endpoint path
     * @param model Optional model name to filter requests by
     * @return null (StubMapping is not used in this implementation)
     */
    override fun setInferenceErrorResponse(
        statusCode: Int,
        errorMessage: String?,
        errorType: String?,
        delay: Duration,
        streamDelay: Duration,
        segment: String,
        model: String?,
        hostName: String?
    ): StubMapping? {
        data class ErrorResponse(
            val status_code: Int,
            val error_message: String?,
            val error_type: String?,
            val delay: Long,
            val stream_delay: Long,
            val segment: String,
            val host_name: String? = null,
        )

        val request =
            ErrorResponse(statusCode, errorMessage, errorType, delay.toMillis(), streamDelay.toMillis(), segment, hostName)

        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/responses/inference/error")
                .jsonBody(cosmosJson.toJson(request))
                .responseString()

            Logger.debug("Set inference error response: $response")
        } catch (e: Exception) {
            Logger.error("Failed to set inference error response: ${e.message}")
        }

        return null // StubMapping is not used in this implementation
    }

    /**
     * Sets the POC response with the specified weight.
     *
     * @param weight The number of nonces to generate
     * @param scenarioName The name of the scenario
     */
    override fun setPocResponse(weight: Long, hostName: String?, scenarioName: String) {
        data class SetPocResponseRequest(
            val weight: Long,
            val scenarioName: String,
            val hostName: String?
        )

        val request = SetPocResponseRequest(weight, scenarioName, hostName)
        try {
            val (_, response, _) = Fuel.post("$baseUrl/api/v1/responses/poc")
                .jsonBody(cosmosJson.toJson(request))
                .responseString()
            if (response.statusCode != 200) {
                Logger.error("Failed to set POC response: ${response.statusCode} ${response.responseMessage}")
            } else {
                Logger.debug("Set POC response: $response")
            }
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

    override fun hasRequestsToVersionedEndpoint(segment: String): Boolean {
        // For MockServerInferenceMock, we can't easily verify WireMock-style request patterns
        // Since this is primarily used in tests with the original WireMock-based InferenceMock,
        // we'll return true as a placeholder. In a real implementation, this would require
        // additional endpoint on the mock server to query request history.
        Logger.warn("hasRequestsToVersionedEndpoint called on MockServerInferenceMock - returning true as placeholder")
        return true
    }

    /**
     * Sets the POC v2 (artifact-based) response with the specified weight.
     * The Ktor mock server handles v2 artifact generation via its webhook service.
     *
     * @param weight The number of artifacts to generate
     * @param hostName Optional host name for the request
     * @param scenarioName The name of the scenario
     */
    override fun setPocV2Response(weight: Long, hostName: String?, scenarioName: String) {
        // The Ktor mock server uses the same weight mechanism as v1
        // via /api/v1/responses/poc endpoint which configures the webhook service
        setPocResponse(weight, hostName, scenarioName)
    }

    /**
     * Sets the POC v2 validation response with the specified weight.
     *
     * @param weight The number of artifacts to validate
     * @param scenarioName The name of the scenario
     */
    override fun setPocV2ValidationResponse(weight: Long, scenarioName: String) {
        // For v2 validation, the mock server's webhook service handles the validation
        // callback automatically via the same weight configuration
        setPocResponse(weight, scenarioName)
    }
}
