package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState

/**
 * Configures routes for health-related endpoints.
 */
fun Route.healthRoutes() {
    // GET /health - Returns 200 OK if the state is INFERENCE
    get("/health") {
        handleHealthRequest(call)
    }
    
    // Versioned endpoint: GET /{version}/health - Returns 200 OK if the state is INFERENCE
    get("/{version}/health") {
        handleHealthRequest(call)
    }
}

/**
 * Handles health requests for both versioned and non-versioned endpoints
 */
private suspend fun handleHealthRequest(call: ApplicationCall) {
    // This endpoint requires the state to be INFERENCE
    if (ModelState.getCurrentState() != ModelState.INFERENCE) {
        call.respond(HttpStatusCode.ServiceUnavailable)
        return
    }
    
    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}