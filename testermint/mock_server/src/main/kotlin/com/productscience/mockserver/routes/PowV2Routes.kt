package com.productscience.mockserver.routes

import com.productscience.mockserver.getHost
import com.productscience.mockserver.model.*
import com.productscience.mockserver.service.HostName
import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.productscience.mockserver.service.WebhookService
import org.slf4j.LoggerFactory

/**
 * Configures routes for PoC v2 (artifact-based) endpoints.
 * These are the /api/v1/inference/pow/ endpoints (init/generate, generate, status, stop) that proxy through MLNode to vLLM.
 */
fun Route.powV2Routes(webhookService: WebhookService) {
    val logger = LoggerFactory.getLogger("PowV2Routes")

    // POST /api/v1/inference/pow/init/generate - Fan-out generate to all backends
    post("/api/v1/inference/pow/init/generate") {
        handleInitGenerateV2(call, webhookService, logger)
    }

    // Versioned POST /{version}/api/v1/inference/pow/init/generate
    post("/{version}/api/v1/inference/pow/init/generate") {
        val version = call.parameters["version"]
        logger.info("Received versioned PoC v2 init/generate request for version: $version")
        handleInitGenerateV2(call, webhookService, logger)
    }

    // POST /api/v1/inference/pow/generate - Generate/validate specific nonces
    post("/api/v1/inference/pow/generate") {
        handleGenerateV2(call, webhookService, logger)
    }

    // Versioned POST /{version}/api/v1/inference/pow/generate
    post("/{version}/api/v1/inference/pow/generate") {
        val version = call.parameters["version"]
        logger.info("Received versioned PoC v2 generate request for version: $version")
        handleGenerateV2(call, webhookService, logger)
    }

    // GET /api/v1/inference/pow/status - Aggregate status from all backends
    get("/api/v1/inference/pow/status") {
        handlePowStatusV2(call, logger)
    }

    // Versioned GET /{version}/api/v1/inference/pow/status
    get("/{version}/api/v1/inference/pow/status") {
        val version = call.parameters["version"]
        logger.debug("Received versioned PoC v2 status request for version: $version")
        handlePowStatusV2(call, logger)
    }

    // POST /api/v1/inference/pow/stop - Fan-out stop to all backends
    post("/api/v1/inference/pow/stop") {
        handlePowStopV2(call, logger)
    }

    // Versioned POST /{version}/api/v1/inference/pow/stop
    post("/{version}/api/v1/inference/pow/stop") {
        val version = call.parameters["version"]
        logger.info("Received versioned PoC v2 stop request for version: $version")
        handlePowStopV2(call, logger)
    }
}

/**
 * Handles PoC v2 init/generate requests.
 * Triggers webhook callback to /generated with artifact batches.
 *
 * PoC v2 does NOT require a global stop before starting generation.
 * This relaxes the v1 requirement for STOPPED state, allowing v2 to work
 * without calling Stop() as per the plan.
 */
private suspend fun handleInitGenerateV2(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
    logger.info("Received PoC v2 init/generate request")
    
    val host = call.getHost()
    val currentModelState = getModelState(host)
    val currentPowState = getPowState(host)
    
    // PoC v2: Relaxed precondition - allow starting without requiring STOPPED state.
    // This supports the "no Stop()" requirement from the plan.
    // We still return success if already generating (idempotency).
    if (currentPowState == PowState.POW_GENERATING) {
        logger.info("PoC v2 init/generate: Already generating, returning success (idempotent)")
        call.respond(HttpStatusCode.OK, mapOf(
            "status" to "OK",
            "backends" to 1,
            "n_groups" to 1,
            "message" to "Already generating"
        ))
        return
    }

    logger.info("PoC v2 init/generate: Transitioning from state=$currentModelState, powState=$currentPowState to POW/POW_GENERATING")
    setModelState(host, ModelState.POW)
    setPowState(host, PowState.POW_GENERATING)
    logger.info("State updated to POW with POW_GENERATING substate (v2)")

    val requestBody = call.receiveText()
    logger.debug("Processing PoC v2 generate webhook with request body: $requestBody")

    // Process the webhook asynchronously - sends artifacts to callback URL
    webhookService.processGeneratePocV2Webhook(requestBody, HostName(call.getHost()))

    call.respond(HttpStatusCode.OK, mapOf(
        "status" to "OK",
        "backends" to 1,
        "n_groups" to 1
    ))
}

/**
 * Handles PoC v2 /generate requests (validation flow).
 * Triggers webhook callback to /validated with validation results.
 *
 * PoC v2: Relaxed state requirements to support the "no Stop()" flow.
 */
private suspend fun handleGenerateV2(call: ApplicationCall, webhookService: WebhookService, logger: org.slf4j.Logger) {
    logger.info("Received PoC v2 generate (validation) request")

    val host = call.getHost()
    val currentModelState = getModelState(host)
    val currentPowState = getPowState(host)
    
    // PoC v2: Accept validation requests in any state. This supports the flow
    // where validators send /generate with validation.artifacts without requiring
    // the node to be in a specific state.
    logger.info("PoC v2 generate: Current state=$currentModelState, powState=$currentPowState")

    // Transition to POW/VALIDATING if not already in POW state
    if (currentModelState != ModelState.POW) {
        setModelState(host, ModelState.POW)
    }
    setPowState(host, PowState.POW_VALIDATING)

    val requestBody = call.receiveText()
    logger.debug("Processing PoC v2 validation webhook with request body: $requestBody")

    // Process the webhook asynchronously - sends validation result to callback URL
    webhookService.processValidatePocV2Webhook(requestBody)

    call.respond(HttpStatusCode.OK, mapOf(
        "status" to "completed",
        "request_id" to "mock-validation-request-id"
    ))
}

/**
 * Handles PoC v2 status requests.
 */
private suspend fun handlePowStatusV2(call: ApplicationCall, logger: org.slf4j.Logger) {
    logger.debug("Received PoC v2 status request")
    val powState = getPowState(call.getHost())
    val statusStr = when (powState) {
        PowState.POW_GENERATING -> "GENERATING"
        PowState.POW_VALIDATING -> "IDLE"
        else -> "IDLE"
    }
    call.respond(
        HttpStatusCode.OK,
        mapOf(
            "status" to statusStr,
            "backends" to listOf(
                mapOf("port" to 5001, "status" to statusStr)
            )
        )
    )
}

/**
 * Handles PoC v2 stop requests.
 */
private suspend fun handlePowStopV2(call: ApplicationCall, logger: org.slf4j.Logger) {
    logger.info("Received PoC v2 stop request")
    
    val host = call.getHost()
    setModelState(host, ModelState.STOPPED)
    setPowState(host, PowState.POW_STOPPED)

    call.respond(HttpStatusCode.OK, mapOf(
        "status" to "OK",
        "results" to listOf(mapOf("port" to 5001, "status" to "stopped")),
        "errors" to null
    ))
}

