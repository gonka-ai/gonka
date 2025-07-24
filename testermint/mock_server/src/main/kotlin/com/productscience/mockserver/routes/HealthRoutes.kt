package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import org.slf4j.LoggerFactory

/**
 * Configures routes for health-related endpoints.
 */
fun Route.healthRoutes() {
    val logger = LoggerFactory.getLogger("HealthRoutes")

    // GET /health - Returns 200 OK if the state is INFERENCE
    get("/health") {
        handleHealthCheck(call, logger)
    }

    // Versioned GET /{version}/health - Returns 200 OK if the state is INFERENCE
    get("/{version}/health") {
        val version = call.parameters["version"]
        logger.debug("Received versioned health check request for version: $version")
        handleHealthCheck(call, logger)
    }
}

/**
 * Handles health check requests.
 */
private suspend fun handleHealthCheck(call: ApplicationCall, logger: org.slf4j.Logger) {
    // This endpoint requires the state to be INFERENCE
    if (ModelState.getCurrentState() != ModelState.INFERENCE) {
        call.respond(HttpStatusCode.ServiceUnavailable)
        return
    }
    
    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}