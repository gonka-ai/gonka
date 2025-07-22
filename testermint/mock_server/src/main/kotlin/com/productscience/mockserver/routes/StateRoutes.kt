package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState

/**
 * Configures routes for state-related endpoints.
 */
fun Route.stateRoutes() {
    // Original endpoint: GET /api/v1/state - Returns the current state of the model
    get("/api/v1/state") {
        handleStateRequest(call)
    }
    
    // Versioned endpoint: GET /{version}/api/v1/state - Returns the current state of the model
    get("/{version}/api/v1/state") {
        handleStateRequest(call)
    }
}

/**
 * Handles state requests for both versioned and non-versioned endpoints
 */
private suspend fun handleStateRequest(call: ApplicationCall) {
    val currentState = ModelState.getCurrentState()
    call.respond(
        HttpStatusCode.OK,
        mapOf("state" to currentState.name)
    )
}