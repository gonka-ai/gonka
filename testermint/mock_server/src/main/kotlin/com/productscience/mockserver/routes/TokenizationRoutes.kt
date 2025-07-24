package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.fasterxml.jackson.annotation.JsonProperty
import org.slf4j.LoggerFactory
import com.productscience.mockserver.service.TokenizationService

/**
 * Data class for tokenization request
 */
data class TokenizationRequest(
    val model: String,
    val prompt: String
)

/**
 * Data class for tokenization response
 */
data class TokenizationResponse(
    val count: Int,
    @JsonProperty("max_model_len")
    val maxModelLen: Int,
    val tokens: List<Int>
)

/**
 * Configures routes for tokenization-related endpoints.
 */
fun Route.tokenizationRoutes(tokenizationService: TokenizationService) {
    val logger = LoggerFactory.getLogger("TokenizationRoutes")

    // POST /tokenize - Tokenizes the provided prompt
    post("/tokenize") {
        handleTokenization(call, tokenizationService, logger)
    }

    // Versioned POST /{version}/tokenize - Tokenizes the provided prompt
    post("/{version}/tokenize") {
        val version = call.parameters["version"]
        logger.info("Received versioned tokenization request for version: $version")
        handleTokenization(call, tokenizationService, logger)
    }

    // Legacy route with optional segment for backwards compatibility
    route("{segment?}") {
        post("/tokenize") {
            handleTokenization(call, tokenizationService, logger)
        }
    }
}

/**
 * Handles tokenization requests.
 */
private suspend fun handleTokenization(call: ApplicationCall, tokenizationService: TokenizationService, logger: org.slf4j.Logger) {
    try {
        val request = call.receive<TokenizationRequest>()
        logger.info("Received tokenization request for model: ${request.model}")

        val tokenizationResult = tokenizationService.tokenize(request.model, request.prompt)

        call.respond(HttpStatusCode.OK, tokenizationResult)
    } catch (e: Exception) {
        logger.error("Error processing tokenization request: ${e.message}", e)
        call.respond(
            HttpStatusCode.BadRequest,
            mapOf(
                "error" to "Invalid request format or processing error"
            )
        )
    }
}