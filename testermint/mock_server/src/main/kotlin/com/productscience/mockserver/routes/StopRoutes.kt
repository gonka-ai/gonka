package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState

/**
 * Configures routes for stop-related endpoints.
 */
fun Route.stopRoutes() {
    // POST /api/v1/stop - Transitions to STOPPED state
    post("/api/v1/stop") {
        // Update the state to STOPPED
        ModelState.updateState(ModelState.STOPPED)
        
        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }
}