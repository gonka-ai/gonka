package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.PowState

/**
 * Configures routes for stop-related endpoints.
 */
fun Route.stopRoutes() {
    // Original endpoint: POST /api/v1/stop - Transitions to STOPPED state
    post("/api/v1/stop") {
        handleStopRequest(call)
    }
    
    // Versioned endpoint: POST /{version}/api/v1/stop - Transitions to STOPPED state
    post("/{version}/api/v1/stop") {
        handleStopRequest(call)
    }
}

/**
 * Handles stop requests for both versioned and non-versioned endpoints
 */
private suspend fun handleStopRequest(call: ApplicationCall) {
    // Update the state to STOPPED
    ModelState.updateState(ModelState.STOPPED)
    PowState.updateState(PowState.POW_STOPPED)
    
    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}