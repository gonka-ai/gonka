package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.PowState
import com.productscience.mockserver.service.WebhookService
import org.slf4j.LoggerFactory

/**
 * Configures routes for POW-related endpoints.
 */
fun Route.powRoutes(webhookService: WebhookService) {
    val logger = LoggerFactory.getLogger("PowRoutes")

    // Original endpoints
    post("/api/v1/pow/init/generate") {
        handlePowInitGenerate(call, webhookService, logger)
    }
    
    post("/api/v1/pow/init/validate") {
        handlePowInitValidate(call, logger)
    }
    
    post("/api/v1/pow/validate") {
        handlePowValidate(call, webhookService, logger)
    }
    
    get("/api/v1/pow/status") {
        handlePowStatus(call, logger)
    }

    // Versioned endpoints
    post("/{version}/api/v1/pow/init/generate") {
        handlePowInitGenerate(call, webhookService, logger)
    }
    
    post("/{version}/api/v1/pow/init/validate") {
        handlePowInitValidate(call, logger)
    }
    
    post("/{version}/api/v1/pow/validate") {
        handlePowValidate(call, webhookService, logger)
    }
    
    get("/{version}/api/v1/pow/status") {
        handlePowStatus(call, logger)
    }
}

/**
 * Handles POW init/generate requests for both versioned and non-versioned endpoints
 */
private suspend fun handlePowInitGenerate(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
    logger.info("Received POW init/generate request")
    
    // Update the state to POW
    if (ModelState.getCurrentState() != ModelState.STOPPED ||
        PowState.getCurrentState() != PowState.POW_STOPPED) {
        logger.warn("Invalid state for POW init/generate. Current state: ${ModelState.getCurrentState()}, POW state: ${PowState.getCurrentState()}")
        call.respond(HttpStatusCode.BadRequest, mapOf(
            "error" to "Invalid state for validation. state = ${ModelState.POW}. powState = ${PowState.POW_GENERATING}"
        ))
        return
    }

    ModelState.updateState(ModelState.POW)
    PowState.updateState(PowState.POW_GENERATING)
    logger.info("State updated to POW with POW_GENERATING substate")

    // Get the request body
    val requestBody = call.receiveText()
    logger.debug("Processing generate POC webhook with request body: $requestBody")

    // Process the webhook asynchronously
    webhookService.processGeneratePocWebhook(requestBody)

    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}

/**
 * Handles POW init/validate requests for both versioned and non-versioned endpoints
 */
private suspend fun handlePowInitValidate(call: ApplicationCall, logger: org.slf4j.Logger) {
    logger.info("Received POW init/validate request")
    
    // This endpoint requires the state to be POW
    if (ModelState.getCurrentState() != ModelState.POW ||
        PowState.getCurrentState() != PowState.POW_GENERATING) {
        logger.warn("Invalid state for POW init/validate. Current state: ${ModelState.getCurrentState()}, POW state: ${PowState.getCurrentState()}")
        call.respond(HttpStatusCode.BadRequest, mapOf(
            "error" to "Invalid state for validation. state = ${ModelState.POW}. powState = ${PowState.POW_GENERATING}"
        ))
        return
    }

    PowState.updateState(PowState.POW_VALIDATING)
    logger.info("POW state updated to POW_VALIDATING")

    call.receiveText()

    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}

/**
 * Handles POW validate requests for both versioned and non-versioned endpoints
 */
private suspend fun handlePowValidate(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
    logger.info("Received POW validate batch request")
    
    // This endpoint requires the state to be POW
    if (ModelState.getCurrentState() != ModelState.POW ||
        PowState.getCurrentState() != PowState.POW_VALIDATING) {
        logger.warn("Invalid state for POW validate batch. Current state: ${ModelState.getCurrentState()}, POW state: ${PowState.getCurrentState()}")
        call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for batch validation"))
        return
    }

    // Get the request body
    val requestBody = call.receiveText()
    logger.debug("Processing validate POC batch webhook with request body: $requestBody")

    // Process the webhook asynchronously
    webhookService.processValidatePocBatchWebhook(requestBody)

    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}

/**
 * Handles POW status requests for both versioned and non-versioned endpoints
 */
private suspend fun handlePowStatus(call: ApplicationCall, logger: org.slf4j.Logger) {
    logger.debug("Received POW status request")
    // Respond with the current state
    call.respond(
        HttpStatusCode.OK,
        mapOf(
            "status" to PowState.getCurrentState(),
            "is_model_initialized" to false // FIXME: hardcoded for now, should be replaced with actual logic
        )
    )
}
