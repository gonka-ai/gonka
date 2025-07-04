package com.productscience.mockserver.routes

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.service.TokenizationService
import io.ktor.client.request.*
import io.ktor.client.statement.*
import io.ktor.http.*
import io.ktor.server.testing.*
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Assertions.*
import io.ktor.server.application.*
import io.ktor.server.routing.*
import io.ktor.server.plugins.contentnegotiation.*
import io.ktor.serialization.jackson.*

class TokenizationRoutesTest {

    private val objectMapper = ObjectMapper().registerKotlinModule()

    @Test
    fun `test tokenize endpoint returns correct response`() = testApplication {
        // Configure the test application
        application {
            install(ContentNegotiation) {
                jackson()
            }
            routing {
                tokenizationRoutes(TokenizationService())
            }
        }

        // Create a test request
        val requestBody = TokenizationRequest(
            model = "Qwen/Qwen2.5-7B-Instruct",
            prompt = "This is a prompt"
        )

        // Send the request to the tokenize endpoint
        val response = client.post("/tokenize") {
            contentType(ContentType.Application.Json)
            setBody(objectMapper.writeValueAsString(requestBody))
        }

        // Verify the response
        assertEquals(HttpStatusCode.OK, response.status)
        
        // Parse the response body
        val responseBody = objectMapper.readValue(response.bodyAsText(), TokenizationResponse::class.java)
        
        // Verify the response structure
        assertEquals(4, responseBody.count) // "This", "is", "a", "prompt" = 4 tokens
        assertEquals(32768, responseBody.maxModelLen)
        assertEquals(4, responseBody.tokens.size)
        assertTrue(responseBody.tokens.all { it > 0 })
    }

    @Test
    fun `test tokenize endpoint handles invalid request`() = testApplication {
        // Configure the test application
        application {
            install(ContentNegotiation) {
                jackson()
            }
            routing {
                tokenizationRoutes(TokenizationService())
            }
        }

        // Send an invalid request (missing required fields)
        val response = client.post("/tokenize") {
            contentType(ContentType.Application.Json)
            setBody("{}")
        }

        // Verify the response indicates an error
        assertEquals(HttpStatusCode.BadRequest, response.status)
    }
}