package com.productscience.mockserver.routes

import com.fasterxml.jackson.annotation.JsonProperty
import com.productscience.mockserver.service.AuthTokenService
import com.productscience.mockserver.service.HostName
import io.ktor.http.*
import io.ktor.server.application.*
import io.ktor.server.request.*
import io.ktor.server.response.*
import io.ktor.server.routing.*

data class SetAuthHeaderRequest(
    @JsonProperty("expected_header")
    val expectedHeader: String,
    @JsonProperty("host_name")
    val hostName: String? = null
)

fun Route.authRoutes(authTokenService: AuthTokenService) {
    post("/api/v1/auth/token") {
        try {
            val req = call.receive<SetAuthHeaderRequest>()
            val host = req.hostName?.let { HostName(it) }
            authTokenService.setExpectedHeader(host, req.expectedHeader)
            call.respond(HttpStatusCode.OK, mapOf("status" to "success"))
        } catch (e: Exception) {
            call.respond(HttpStatusCode.BadRequest, mapOf("status" to "error", "message" to e.message))
        }
    }

    post("/api/v1/auth/clear") {
        try {
            val req = call.receive<SetAuthHeaderRequest>()
            val host = req.hostName?.let { HostName(it) }
            authTokenService.clearExpectedHeader(host)
            call.respond(HttpStatusCode.OK, mapOf("status" to "success"))
        } catch (e: Exception) {
            call.respond(HttpStatusCode.BadRequest, mapOf("status" to "error", "message" to e.message))
        }
    }
}