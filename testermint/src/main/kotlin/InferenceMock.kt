package com.productscience

import com.github.tomakehurst.wiremock.client.MappingBuilder
import com.github.tomakehurst.wiremock.client.WireMock
import com.github.tomakehurst.wiremock.client.WireMock.aResponse
import com.github.tomakehurst.wiremock.client.WireMock.equalToJson
import com.github.tomakehurst.wiremock.client.WireMock.post
import com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo
import com.github.tomakehurst.wiremock.http.RequestMethod
import com.github.tomakehurst.wiremock.http.Response.response
import com.github.tomakehurst.wiremock.matching.RequestPatternBuilder
import com.github.tomakehurst.wiremock.stubbing.StubMapping
import com.productscience.data.OpenAIResponse
import org.apache.tuweni.bytes.Bytes.segment
import java.time.Duration

interface IInferenceMock {
    fun setInferenceResponse(
        response: String,
        delay: Duration = Duration.ZERO,
        streamDelay: Duration = Duration.ZERO,
        segment: String = "v3.0.8",
        model: String? = null
    ): StubMapping?
    fun setInferenceResponse(
        openAIResponse: OpenAIResponse,
        delay: Duration = Duration.ZERO,
        streamDelay: Duration = Duration.ZERO,
        segment: String = "v3.0.8",
        model: String? = null
    ): StubMapping?

    fun setInferenceErrorResponse(
        statusCode: Int,
        errorMessage: String? = null,
        errorType: String? = null,
        delay: Duration = Duration.ZERO,
        streamDelay: Duration = Duration.ZERO,
        segment: String = "v3.0.8",
        model: String? = null
    ): StubMapping?

    fun setPocResponse(weight: Long, scenarioName: String = "ModelState")
    fun setPocValidationResponse(weight: Long, scenarioName: String = "ModelState")
    fun getLastInferenceRequest(): InferenceRequestPayload?
    fun hasRequestsToVersionedEndpoint(segment: String): Boolean
}

class InferenceMock(port: Int, val name: String) : IInferenceMock {
    private val mockClient = WireMock(port)
    fun givenThat(builder: MappingBuilder) =
        mockClient.register(builder)
    override fun getLastInferenceRequest(): InferenceRequestPayload? {
        val requests = mockClient.find(RequestPatternBuilder(RequestMethod.POST, urlEqualTo("/v1/chat/completions")))
        if (requests.isEmpty()) {
            return null
        }
        val lastRequest = requests.last()
        return openAiJson.fromJson(lastRequest.bodyAsString, InferenceRequestPayload::class.java)
    }
    override fun setInferenceResponse(response: String, delay: Duration, streamDelay: Duration, segment: String, model: String?): StubMapping? {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segmentPath = if (cleanedSegment != null) "/$cleanedSegment" else ""
        return this.givenThat(
            post(urlEqualTo("${segmentPath}/v1/chat/completions"))
                .apply {
                    if (model != null) {
                        withRequestBody(equalToJson("""{"model": "$model"}""", true, true))
                    }
                }
                .willReturn(
                    aResponse()
                        .withFixedDelay(delay.toMillis().toInt())
                        .withStatus(200)
                        .withBody(response)
                )
        )
    }

    override fun setInferenceResponse(
        openAIResponse: OpenAIResponse,
        delay: Duration,
        streamDelay: Duration,
        segment: String,
        model: String?
    ): StubMapping? =
        this.setInferenceResponse(
            openAiJson.toJson(openAIResponse), delay, streamDelay, segment, model)

    override fun setInferenceErrorResponse(
        statusCode: Int,
        errorMessage: String?,
        errorType: String?,
        delay: Duration,
        streamDelay: Duration,
        segment: String,
        model: String?
    ): StubMapping? {
        // Generate error response body similar to the mock server's ErrorResponse
        val message = errorMessage ?: getDefaultErrorMessage(statusCode)
        val type = errorType ?: getDefaultErrorType(statusCode)
        
        val errorResponseBody = """
            {
              "error": {
                "message": "$message",
                "type": "$type",
                "code": $statusCode
              }
            }
        """.trimIndent()
        
        return this.givenThat(
            post(urlEqualTo("$segment/v1/chat/completions"))
                .apply {
                    if (model != null) {
                        withRequestBody(equalToJson("""{"model": "$model"}""", true, true))
                    }
                }
                .willReturn(
                    aResponse()
                        .withFixedDelay(delay.toMillis().toInt())
                        .withStatus(statusCode)
                        .withHeader("Content-Type", "application/json")
                        .withBody(errorResponseBody)
                )
        )
    }
    
    private fun getDefaultErrorMessage(code: Int): String {
        return when (code) {
            400 -> "Bad Request"
            401 -> "Unauthorized"
            403 -> "Forbidden"
            404 -> "Not Found"
            429 -> "Too Many Requests"
            500 -> "Internal Server Error"
            502 -> "Bad Gateway"
            503 -> "Service Unavailable"
            else -> "Error"
        }
    }
    
    private fun getDefaultErrorType(code: Int): String {
        return when {
            code in 400..499 -> "invalid_request_error"
            code in 500..599 -> "server_error"
            else -> "error"
        }
    }

    override fun setPocResponse(weight: Long, scenarioName: String) {
        // Generate 'weight' number of nonces
        val nonces = (1..weight).toList()
        // Generate distribution values evenly spaced from 0.0 to 1.0
        val dist = nonces.map { it.toDouble() / weight }
        val body = """
            {
              "public_key": "{{jsonPath originalRequest.body '$.public_key'}}",
              "block_hash": "{{jsonPath originalRequest.body '$.block_hash'}}",
              "block_height": {{jsonPath originalRequest.body '$.block_height'}},
              "nonces": $nonces,
              "dist": $dist,
              "received_dist": $dist
            }
        """.trimIndent()
        this.givenThat(
            post(urlEqualTo("/api/v1/pow/init/generate"))
                .inScenario("ModelState")
                .willSetStateTo("POW")
                .willReturn(
                    aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("")
                )
                .withPostServeAction(
                    "webhook",
                    mapOf(
                        "method" to "POST",
                        "url" to "{{jsonPath originalRequest.body '$.url'}}/generated",
                        "headers" to mapOf("Content-Type" to "application/json"),
                        "delay" to mapOf("type" to "fixed", "milliseconds" to 1000),
                        "body" to body
                    )
                )
        )

    }

    override fun setPocValidationResponse(weight: Long, scenarioName: String) {
        // Generate 'weight' number of nonces
        val nonces = (1..weight).toList()
        // Generate distribution values evenly spaced from 0.0 to 1.0
        val dist = nonces.map { it.toDouble() / weight }
        val callbackBody = """
            {
              "public_key": "{{jsonPath originalRequest.body '$.public_key'}}",
              "block_hash": "{{jsonPath originalRequest.body '$.block_hash'}}",
              "block_height": {{jsonPath originalRequest.body '$.block_height'}},
              "nonces": $nonces,
              "dist": $dist,
              "received_dist": $dist,
              "r_target": {{jsonPath originalRequest.body '$.r_target'}},
              "fraud_threshold": {{jsonPath originalRequest.body '$.fraud_threshold'}},
              "n_invalid": 0,
              "probability_honest": 0.99,
              "fraud_detected": false
            }
        """.trimIndent()

        this.givenThat(
            post(urlEqualTo("/api/v1/pow/init/validate"))
                .inScenario(scenarioName)
                .whenScenarioStateIs("POW") // Assuming this is the required state as per validate_poc.json
                .willSetStateTo("POW_VALIDATE") // Transition state so batch validation template can match
                .willReturn(
                    aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("") // Or any immediate response body if needed
                )
                .withPostServeAction(
                    "webhook",
                    mapOf(
                        "method" to "POST",
                        "url" to "{{jsonPath originalRequest.body '$.url'}}/validated",
                        "headers" to mapOf("Content-Type" to "application/json"),
                        "delay" to mapOf("type" to "fixed", "milliseconds" to 5000), // Adjust delay as needed
                        "body" to callbackBody
                    )
                )
        )
    }

    override fun hasRequestsToVersionedEndpoint(segment: String): Boolean {
        val cleanedSegment = segment.trim('/').takeIf { it.isNotEmpty() }
        val segmentPath = if (cleanedSegment != null) "/$cleanedSegment" else ""
        val requests = mockClient.find(RequestPatternBuilder(RequestMethod.POST, urlEqualTo("${segmentPath}/v1/chat/completions")))
        return requests.isNotEmpty()
    }
}
