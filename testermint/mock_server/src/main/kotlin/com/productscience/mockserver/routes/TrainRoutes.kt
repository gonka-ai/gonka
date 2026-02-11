package com.productscience.mockserver.routes

import com.productscience.mockserver.getHost
import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.getModelState
import com.productscience.mockserver.model.setModelState

/**
 * Configures routes for training-related endpoints.
 */
fun Route.trainRoutes() {
    // POST /api/v1/train/start - Transitions to TRAIN state
    post("/api/v1/train/start") {
        val host = call.getHost()
        // This endpoint requires the state to be STOPPED
        if (getModelState(host) != ModelState.STOPPED) {
            call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for train start"))
            return@post
        }
        
        // Update the state to TRAIN
        setModelState(host, ModelState.TRAIN)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }
}