package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.service.ResponseService
import kotlinx.coroutines.delay
import org.slf4j.LoggerFactory

/**
 * Configures routes for inference-related endpoints.
 */
fun Route.inferenceRoutes(responseService: ResponseService) {
    // POST /api/v1/inference/up - Transitions to INFERENCE state
    post("/api/v1/inference/up") {
        // This endpoint requires the state to be STOPPED
        if (ModelState.getCurrentState() != ModelState.STOPPED) {
            call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for inference up"))
            return@post
        }

        // Update the state to INFERENCE
        ModelState.updateState(ModelState.INFERENCE)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    // Handle the exact path /v1/chat/completions
    post("/v1/chat/completions") {
        handleChatCompletions(call, responseService)
    }

    // Handle paths with a segment prefix before /v1/chat/completions
    // This will match paths like /api/v1/chat/completions, /custom/v1/chat/completions, etc.
    post("/{segment}/v1/chat/completions") {
        handleChatCompletions(call, responseService)
    }

    // Handle paths with multiple segments in the prefix
    // This will match paths like /api/v2/v1/chat/completions, /custom/path/v1/chat/completions, etc.
    post("/{...segments}/v1/chat/completions") {
        handleChatCompletions(call, responseService)
    }
}

/**
 * Handles chat completions requests.
 */
private suspend fun handleChatCompletions(call: ApplicationCall, responseService: ResponseService) {
    val logger = LoggerFactory.getLogger("InferenceRoutes")

    // This endpoint requires the state to be INFERENCE, but we're going to let it go for tests
//    if (ModelState.getCurrentState() != ModelState.INFERENCE) {
//        call.respond(HttpStatusCode.ServiceUnavailable, mapOf("error" to "Service not in INFERENCE state"))
//        return
//    }

    // Get the request body
    val requestBody = call.receiveText()
    logger.info("Received chat completion request for path: ${call.request.path()}")

    // Get the endpoint path
    val path = call.request.path()

    // Get the response from the ResponseService
    val responseData = responseService.getInferenceResponse(path)
    logger.info("Retrieved response data for path $path: ${responseData != null}")

    if (responseData != null) {
        val (responseBody, delayMs) = responseData
        logger.info("Using configured response with delay: ${delayMs}ms")

        // Apply delay if specified
        if (delayMs > 0) {
            delay(delayMs.toLong())
        }

        // Set content type to application/json
        call.response.header("Content-Type", "application/json")

        // Respond with the stored response
        logger.info("Responding with configured response: $responseBody")
        call.respondText(responseBody, ContentType.Application.Json)
    } else {
        logger.warn("No configured response found, using default response")
        // If no response is set, return a default response
        call.respond(
            HttpStatusCode.OK,
            mapOf(
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
        )
    }
}
