package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.productscience.mockserver.model.ModelState
import com.productscience.mockserver.model.PowState
import com.productscience.mockserver.service.WebhookService

/**
 * Configures routes for POW-related endpoints.
 */
fun Route.powRoutes(webhookService: WebhookService) {
    // POST /api/v1/pow/init/generate - Generates POC and transitions to POW state
    post("/api/v1/pow/init/generate") {
        // Update the state to POW
        if (ModelState.getCurrentState() != ModelState.STOPPED ||
            PowState.getCurrentState() != PowState.POW_STOPPED) {
            call.respond(HttpStatusCode.BadRequest, mapOf(
                "error" to "Invalid state for validation. state = ${ModelState.POW}. powState = ${PowState.POW_GENERATING}"
            ))
            return@post
        }

        ModelState.updateState(ModelState.POW)
        PowState.updateState(PowState.POW_GENERATING)

        // Get the request body
        val requestBody = call.receiveText()

        // Process the webhook asynchronously
        webhookService.processGeneratePocWebhook(requestBody)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    // POST /api/v1/pow/init/validate - Validates POC
    post("/api/v1/pow/init/validate") {
        // This endpoint requires the state to be POW
        if (ModelState.getCurrentState() != ModelState.POW ||
            PowState.getCurrentState() != PowState.POW_GENERATING) {
            call.respond(HttpStatusCode.BadRequest, mapOf(
                "error" to "Invalid state for validation. state = ${ModelState.POW}. powState = ${PowState.POW_GENERATING}"
            ))
            return@post
        }

        PowState.updateState(PowState.POW_VALIDATING)

        call.receiveText()

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    // POST /api/v1/pow/validate - Validates POC batch
    post("/api/v1/pow/validate") {
        // This endpoint requires the state to be POW
        if (ModelState.getCurrentState() != ModelState.POW ||
            PowState.getCurrentState() != PowState.POW_VALIDATING) {
            call.respond(HttpStatusCode.BadRequest, mapOf("error" to "Invalid state for batch validation"))
            return@post
        }

        // Get the request body
        val requestBody = call.receiveText()

        // Process the webhook asynchronously
        webhookService.processValidatePocBatchWebhook(requestBody)

        // Respond with 200 OK
        call.respond(HttpStatusCode.OK)
    }

    get("/api/v1/pow/state") {
        // Respond with the current state
        call.respond(
            HttpStatusCode.OK,
            mapOf(
                "status" to PowState.getCurrentState(),
                "is_model_initialized" to false // FIXME: hardcoded for now, should be replaced with actual logic
            )
        )
    }
}
