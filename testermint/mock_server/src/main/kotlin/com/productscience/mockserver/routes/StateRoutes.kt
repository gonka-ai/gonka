package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import org.slf4j.LoggerFactory

/**
 * Configures routes for state-related endpoints.
 */
fun Route.stateRoutes() {
    val logger = LoggerFactory.getLogger("StateRoutes")

    // GET /api/v1/state - Returns the current state of the model
    get("/api/v1/state") {
        handleStateRequest(call, logger)
    }

    // Versioned GET /{version}/api/v1/state - Returns the current state of the model
    get("/{version}/api/v1/state") {
        val version = call.parameters["version"]
        logger.debug("Received versioned state request for version: $version")
        handleStateRequest(call, logger)
    }
}

/**
 * Handles state requests.
 */
private suspend fun handleStateRequest(call: ApplicationCall, logger: org.slf4j.Logger) {
    val currentState = ModelState.getCurrentState()
    call.respond(
        HttpStatusCode.OK,
        mapOf("state" to currentState.name)
    )
}