package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.getHost
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.getModelState
import com.productscience.mockserver.model.setModelState
import com.productscience.mockserver.service.ResponseService
import com.productscience.mockserver.service.SSEService
import com.productscience.mockserver.service.HostName
import com.productscience.mockserver.service.AuthTokenService
import io.ktor.http.HttpHeaders
import kotlinx.coroutines.delay
import org.slf4j.LoggerFactory

/**
 * Configures routes for inference-related endpoints.
 */
fun Route.inferenceRoutes(responseService: ResponseService, authTokenService: AuthTokenService, sseService: SSEService = SSEService()) {
    // POST /api/v1/inference/up - Transitions to INFERENCE state
    post("/api/v1/inference/up") {
        val logger = LoggerFactory.getLogger("InferenceRoutes")
        logger.info("Received inference/up request")

        val host = call.getHost()
        // This endpoint requires the state to be STOPPED
        if (getModelState(host) != ModelState.STOPPED) {
            call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for inference up"))
            return@post
        }

        // Update the state to INFERENCE
        setModelState(host, ModelState.INFERENCE)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    // Handle all versioned inference/up endpoints
    post("/{...segments}/api/v1/inference/up") {
        val segments = call.parameters.getAll("segments")
        val version = segments?.firstOrNull() // If there's a version, it would be the first segment
        val logger = LoggerFactory.getLogger("InferenceRoutes")
        logger.info("Received inference/up request" + if (version != null) " for version: $version" else "")

        val host = call.getHost()
        // This endpoint requires the state to be STOPPED
        if (getModelState(host) != ModelState.STOPPED) {
            call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for inference up"))
            return@post
        }

        // Update the state to INFERENCE
        setModelState(host, ModelState.INFERENCE)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    // Handle the exact path /v1/chat/completions
    post("/v1/chat/completions") {
        handleChatCompletions(call, responseService, authTokenService, sseService)
    }
    // Handle all versioned chat completions endpoints
    post("/{...segments}/v1/chat/completions") {
        handleChatCompletions(call, responseService, authTokenService, sseService)
    }
}

/**
 * Handles chat completions requests.
 */
private suspend fun handleChatCompletions(call: ApplicationCall, responseService: ResponseService, authTokenService: AuthTokenService, sseService: SSEService) {
    val logger = LoggerFactory.getLogger("InferenceRoutes")
    val objectMapper = ObjectMapper()
        .registerKotlinModule()
        .setPropertyNamingStrategy(com.fasterxml.jackson.databind.PropertyNamingStrategies.SNAKE_CASE)

    // This endpoint requires the state to be INFERENCE, but we're going to let it go for tests
//    if (ModelState.getCurrentState() != ModelState.INFERENCE) {
//        call.respond(HttpStatusCode.ServiceUnavailable, mapOf("error" to "Service not in INFERENCE state"))
//        return
//    }

    // Enforce Authorization header if configured for this host
    val hostName = HostName(call.getHost())
    val expectedAuth = authTokenService.getExpectedHeader(hostName)
    val authHeader = call.request.headers[HttpHeaders.Authorization]
    if (expectedAuth != null) {
        val requireOnly = expectedAuth == "*"
        val valid = if (requireOnly) {
            authHeader != null && authHeader.isNotBlank()
        } else {
            authHeader == expectedAuth
        }
        if (!valid) {
            logger.warn("Unauthorized request for host {}. Expected Authorization: {} but got: {}", hostName.name, expectedAuth, authHeader)
            call.response.header("Content-Type", "application/json")
            call.respondText(
                """{"error":"unauthorized","message":"Authorization header missing or invalid"}""",
                ContentType.Application.Json,
                HttpStatusCode.Unauthorized
            )
            return
        }
    }

    // Get the request body
    val requestBody = call.receiveText()
    logger.info("Received chat completion request for path: ${call.request.path()}")

    // Store the last inference request
    responseService.setLastInferenceRequest(requestBody)
    logger.info("Stored last inference request")

    // Extract model from request body
    var model: String? = null
    try {
        val requestJson = objectMapper.readTree(requestBody)
        model = requestJson.get("model")?.asText()
        logger.info("Extracted model from request: $model")
    } catch (e: Exception) {
        logger.warn("Failed to extract model from request: ${e.message}")
    }

    // Check if streaming is requested
    val isStreaming = sseService.isStreamingRequested(requestBody)
    logger.info("Streaming requested: $isStreaming")

    // Get the endpoint path
    val path = call.request.path()

    // Get the response configuration from the ResponseService (per-host)
    val responseConfig = responseService.getInferenceResponseConfig(path, model, hostName)
    logger.info("Retrieved response config for path $path: ${responseConfig != null}")

    // Default stream delay if not provided in the response
    var streamDelayMs = 0L

    when (responseConfig) {
        is com.productscience.mockserver.service.ResponseConfig.Success -> {
            val responseBody = responseConfig.responseBody
            val delayMs = responseConfig.delay
            streamDelayMs = responseConfig.streamDelay
            logger.info("Using configured success response with delay: ${delayMs}ms, stream delay: ${streamDelayMs}ms")

            // Apply delay if specified
            if (delayMs > 0) {
                delay(delayMs.toLong())
            }

            if (isStreaming) {
                // Stream the response using SSE
                logger.info("Streaming response using SSE with delay: ${streamDelayMs}ms")
                sseService.streamResponse(call, responseBody, streamDelayMs)
            } else {
                // Set content type to application/json for non-streaming response
                call.response.header("Content-Type", "application/json")

                // Respond with the stored response
                logger.info("Responding with configured response: $responseBody")
                call.respondText(responseBody, ContentType.Application.Json)
            }
        }
        is com.productscience.mockserver.service.ResponseConfig.Error -> {
            val errorResponse = responseConfig.errorResponse
            val delayMs = responseConfig.delay
            logger.info("Using configured error response with status ${errorResponse.statusCode} and delay: ${delayMs}ms")

            // Apply delay if specified
            if (delayMs > 0) {
                delay(delayMs.toLong())
            }

            // Set content type to application/json
            call.response.header("Content-Type", "application/json")

            // Respond with the error
            logger.info("Responding with error status ${errorResponse.statusCode}: ${errorResponse.toJsonBody()}")
            call.respondText(errorResponse.toJsonBody(), ContentType.Application.Json, HttpStatusCode.fromValue(errorResponse.statusCode))
        }
        null -> {
            logger.warn("No configured response found, using default response")

            // Create a default response
            val defaultResponse = mapOf(
                "choices" to listOf(
                    mapOf(
                        "message" to mapOf(
                            "content" to "This is a default response from the mock server.",
                            "role" to "assistant"
                        ),
                        "finish_reason" to "stop",
                        "index" to 0
                    )
                ),
                "created" to System.currentTimeMillis() / 1000,
                "id" to "mock-${System.currentTimeMillis()}",
                "model" to "mock-model",
                "object" to "chat.completion",
                "usage" to mapOf(
                    "completion_tokens" to 10,
                    "prompt_tokens" to 10,
                    "total_tokens" to 20
                )
            )

            if (isStreaming) {
                // Stream the default response using SSE
                logger.info("Streaming default response using SSE with delay: ${streamDelayMs}ms")
                val defaultResponseJson = objectMapper.writeValueAsString(defaultResponse)
                sseService.streamResponse(call, defaultResponseJson, streamDelayMs)
            } else {
                // Respond with the default response as JSON
                call.respond(HttpStatusCode.OK, defaultResponse)
            }
        }
    }
}
