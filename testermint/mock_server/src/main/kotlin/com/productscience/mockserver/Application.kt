package com.productscience.mockserver

import com.productscience.mockserver.routes.fileRoutes
import com.productscience.mockserver.routes.healthRoutes
import com.productscience.mockserver.routes.inferenceRoutes
import com.productscience.mockserver.routes.powRoutes
import com.productscience.mockserver.routes.responseRoutes
import com.productscience.mockserver.routes.stateRoutes
import com.productscience.mockserver.routes.stopRoutes
import com.productscience.mockserver.routes.tokenizationRoutes
import com.productscience.mockserver.routes.trainRoutes
import com.productscience.mockserver.service.ResponseService
import com.productscience.mockserver.service.TokenizationService
import com.productscience.mockserver.service.WebhookService
import io.ktor.serialization.jackson.jackson
import io.ktor.server.application.Application
import io.ktor.server.application.call
import io.ktor.server.application.install
import io.ktor.server.engine.embeddedServer
import io.ktor.server.netty.Netty
import io.ktor.server.plugins.callloging.CallLogging
import io.ktor.server.plugins.contentnegotiation.ContentNegotiation
import io.ktor.server.request.httpMethod
import io.ktor.server.request.path
import io.ktor.server.response.respond
import io.ktor.server.routing.get
import io.ktor.server.routing.routing
import io.ktor.util.AttributeKey
import org.slf4j.LoggerFactory
import org.slf4j.event.Level

// Define keys for services
val WebhookServiceKey = AttributeKey<WebhookService>("WebhookService")
val ResponseServiceKey = AttributeKey<ResponseService>("ResponseService")
val TokenizationServiceKey = AttributeKey<TokenizationService>("TokenizationService")

fun main() {
    embeddedServer(Netty, port = 8080, host = "0.0.0.0", module = Application::module)
        .start(wait = true)
}

fun Application.module() {
    configureLogging()
    configureSerialization()
    configureServices()
    configureRouting()
}

fun Application.configureLogging() {
    install(CallLogging) {
        level = Level.DEBUG
        filter { call -> true } // Log all requests
        format { call ->
            val status = call.response.status()
            val httpMethod = call.request.httpMethod.value
            val path = call.request.path()
            "Request: $httpMethod $path, Status: $status"
        }
    }
}

fun Application.configureServices() {
    // Create single instances of services to be used by all routes
    val responseService = ResponseService()
    val webhookService = WebhookService(responseService)
    val tokenizationService = TokenizationService()

    // Register the services in the application's attributes
    attributes.put(WebhookServiceKey, webhookService)
    attributes.put(ResponseServiceKey, responseService)
    attributes.put(TokenizationServiceKey, tokenizationService)
}

fun Application.configureRouting() {
    // Get the services from the application's attributes
    val webhookService = attributes[WebhookServiceKey]
    val responseService = attributes[ResponseServiceKey]
    val tokenizationService = attributes[TokenizationServiceKey]

    routing {
        // Server status endpoint
        get("/status") {
            call.respond(
                mapOf(
                    "status" to "ok",
                    "version" to "1.0.1",
                    "timestamp" to System.currentTimeMillis()
                )
            )
        }

        // Register all the route handlers
        stateRoutes()
        powRoutes(webhookService)
        inferenceRoutes(responseService)
        trainRoutes()
        stopRoutes()
        healthRoutes()
        responseRoutes(responseService)
        tokenizationRoutes(tokenizationService)
        fileRoutes() // Route for serving files
    }
}

fun Application.configureSerialization() {
    install(ContentNegotiation) {
        jackson()
    }
}
