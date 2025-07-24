package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.PowState
import org.slf4j.LoggerFactory

/**
 * Configures routes for stop-related endpoints.
 */
fun Route.stopRoutes() {
    val logger = LoggerFactory.getLogger("StopRoutes")

    // POST /api/v1/stop - Transitions to STOPPED state
    post("/api/v1/stop") {
        handleStopRequest(call, logger)
    }

    // Versioned POST /{version}/api/v1/stop - Transitions to STOPPED state
    post("/{version}/api/v1/stop") {
        val version = call.parameters["version"]
        logger.info("Received versioned stop request for version: $version")
        handleStopRequest(call, logger)
    }
}

/**
 * Handles stop requests.
 */
private suspend fun handleStopRequest(call: ApplicationCall, logger: org.slf4j.Logger) {
    logger.info("Received stop request")
    
    // Update the state to STOPPED
    ModelState.updateState(ModelState.STOPPED)
    PowState.updateState(PowState.POW_STOPPED)
    
    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}