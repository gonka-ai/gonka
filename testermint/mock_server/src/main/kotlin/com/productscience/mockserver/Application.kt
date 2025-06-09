package com.productscience.mockserver

import io.ktor.server.application.*
import io.ktor.server.engine.*
import io.ktor.server.netty.*
import io.ktor.server.response.*
import io.ktor.server.routing.*
import io.ktor.http.*
import io.ktor.serialization.jackson.*
import io.ktor.server.plugins.contentnegotiation.*

fun main() {
    embeddedServer(Netty, port = 8080, host = "0.0.0.0", module = Application::module)
        .start(wait = true)
}

fun Application.module() {
    configureRouting()
    configureSerialization()
}

fun Application.configureRouting() {
    routing {
        get("/status") {
            call.respond(
                mapOf(
                    "status" to "ok",
                    "version" to "1.0.0",
                    "timestamp" to System.currentTimeMillis()
                )
            )
        }
    }
}

fun Application.configureSerialization() {
    install(ContentNegotiation) {
        jackson()
    }
}