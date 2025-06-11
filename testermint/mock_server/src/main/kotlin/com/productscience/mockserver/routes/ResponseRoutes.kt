package com.productscience.mockserver.routes

import io.ktor.server.application.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.server.request.*
import io.ktor.http.*
import com.productscience.mockserver.service.ResponseService
import com.productscience.mockserver.model.OpenAIResponse
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule

/**
 * Data class for setting inference response
 */
data class SetInferenceResponseRequest(
    val response: String,
    val delay: Int = 0,
    val segment: String = "",
    val model: String? = null
)

/**
 * Data class for setting POC response
 * 
 * @param weight The number of nonces to generate
 * @param scenarioName The name of the scenario
 */
data class SetPocResponseRequest(
    val weight: Long,
    val scenarioName: String = "ModelState"
)

/**
 * Configures routes for response modification endpoints.
 */
fun Route.responseRoutes(responseService: ResponseService) {
    val objectMapper = ObjectMapper().registerKotlinModule()

    // POST /api/v1/responses/inference - Sets the response for the inference endpoint
    post("/api/v1/responses/inference") {
        try {
            val request = call.receive<SetInferenceResponseRequest>()
            val endpoint = responseService.setInferenceResponse(
                request.response,
                request.delay,
                request.segment,
                request.model
            )

            call.respond(
                HttpStatusCode.OK,
                mapOf(
                    "status" to "success",
                    "message" to "Inference response set successfully",
                    "endpoint" to endpoint
                )
            )
        } catch (e: Exception) {
            call.respond(
                HttpStatusCode.BadRequest,
                mapOf(
                    "status" to "error",
                    "message" to "Failed to set inference response: ${e.message}"
                )
            )
        }
    }

    // POST /api/v1/responses/poc - Sets the POC response with the specified weight
    post("/api/v1/responses/poc") {
        try {
            val request = call.receive<SetPocResponseRequest>()
            responseService.setPocResponse(request.weight, request.scenarioName)

            call.respond(
                HttpStatusCode.OK,
                mapOf(
                    "status" to "success",
                    "message" to "POC response set successfully",
                    "weight" to request.weight,
                    "scenarioName" to request.scenarioName
                )
            )
        } catch (e: Exception) {
            call.respond(
                HttpStatusCode.BadRequest,
                mapOf(
                    "status" to "error",
                    "message" to "Failed to set POC response: ${e.message}"
                )
            )
        }
    }

    // GET /api/v1/responses/inference - Gets all inference responses
    get("/api/v1/responses/inference") {
        try {
            // This is a simplified implementation that just returns success
            // In a real implementation, you would return the actual responses
            call.respond(
                HttpStatusCode.OK,
                mapOf(
                    "status" to "success",
                    "message" to "Inference responses retrieved successfully"
                )
            )
        } catch (e: Exception) {
            call.respond(
                HttpStatusCode.InternalServerError,
                mapOf(
                    "status" to "error",
                    "message" to "Failed to get inference responses: ${e.message}"
                )
            )
        }
    }

    // GET /api/v1/responses/poc - Gets all POC responses
    get("/api/v1/responses/poc") {
        try {
            // This is a simplified implementation that just returns success
            // In a real implementation, you would return the actual responses
            call.respond(
                HttpStatusCode.OK,
                mapOf(
                    "status" to "success",
                    "message" to "POC responses retrieved successfully"
                )
            )
        } catch (e: Exception) {
            call.respond(
                HttpStatusCode.InternalServerError,
                mapOf(
                    "status" to "error",
                    "message" to "Failed to get POC responses: ${e.message}"
                )
            )
        }
    }
}
