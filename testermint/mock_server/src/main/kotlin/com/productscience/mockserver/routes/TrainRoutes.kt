package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState

/**
 * Configures routes for training-related endpoints.
 */
fun Route.trainRoutes() {
    // Original endpoint: POST /api/v1/train/start - Transitions to TRAIN state
    post("/api/v1/train/start") {
        handleTrainStartRequest(call)
    }
    
    // Versioned endpoint: POST /{version}/api/v1/train/start - Transitions to TRAIN state
    post("/{version}/api/v1/train/start") {
        handleTrainStartRequest(call)
    }
}

/**
 * Handles train start requests for both versioned and non-versioned endpoints
 */
private suspend fun handleTrainStartRequest(call: ApplicationCall) {
    // This endpoint requires the state to be STOPPED
    if (ModelState.getCurrentState() != ModelState.STOPPED) {
        call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for train start"))
        return
    }
    
    // Update the state to TRAIN
    ModelState.updateState(ModelState.TRAIN)
    
    // Respond with 200 OK
    call.respond(HttpStatusCode.OK)
}