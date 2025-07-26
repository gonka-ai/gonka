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

    // POST /api/v1/pow/init/generate - Generates POC and transitions to POW state
    post("/api/v1/pow/init/generate") {
        handleInitGenerate(call, webhookService, logger)
    }

    // Versioned POST /{version}/api/v1/pow/init/generate - Generates POC and transitions to POW state
    post("/{version}/api/v1/pow/init/generate") {
        val version = call.parameters["version"]
        logger.info("Received versioned POW init/generate request for version: $version")
        handleInitGenerate(call, webhookService, logger)
    }

    // POST /api/v1/pow/init/validate - Validates POC
    post("/api/v1/pow/init/validate") {
        handleInitValidate(call, logger)
    }

    // Versioned POST /{version}/api/v1/pow/init/validate - Validates POC
    post("/{version}/api/v1/pow/init/validate") {
        val version = call.parameters["version"]
        logger.info("Received versioned POW init/validate request for version: $version")
        handleInitValidate(call, logger)
    }

    // POST /api/v1/pow/validate - Validates POC batch
    post("/api/v1/pow/validate") {
        handleValidateBatch(call, webhookService, logger)
    }

    // Versioned POST /{version}/api/v1/pow/validate - Validates POC batch
    post("/{version}/api/v1/pow/validate") {
        val version = call.parameters["version"]
        logger.info("Received versioned POW validate batch request for version: $version")
        handleValidateBatch(call, webhookService, logger)
    }

    get("/api/v1/pow/status") {
        handlePowStatus(call, logger)
    }

    // Versioned GET /{version}/api/v1/pow/status - Gets POW status
    get("/{version}/api/v1/pow/status") {
        val version = call.parameters["version"]
        logger.debug("Received versioned POW status request for version: $version")
        handlePowStatus(call, logger)
    }
}

/**
 * Handles POW init/generate requests.
 */
private suspend fun handleInitGenerate(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
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
 * Handles POW init/validate requests.
 */
private suspend fun handleInitValidate(call: ApplicationCall, logger: org.slf4j.Logger) {
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
 * Handles POW validate batch requests.
 */
private suspend fun handleValidateBatch(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
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
 * Handles POW status requests.
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
