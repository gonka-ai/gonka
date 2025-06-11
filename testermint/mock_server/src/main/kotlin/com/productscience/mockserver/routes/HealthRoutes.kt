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
        // This endpoint requires the state to be INFERENCE
        if (ModelState.getCurrentState() != ModelState.INFERENCE) {
            call.respond(HttpStatusCode.ServiceUnavailable)
            return@get
        }
        
        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }
}