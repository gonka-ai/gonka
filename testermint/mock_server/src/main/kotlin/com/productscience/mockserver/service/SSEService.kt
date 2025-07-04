package com.productscience.mockserver.service

import com.fasterxml.jackson.databind.JsonNode
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.registerKotlinModule
import com.productscience.mockserver.model.OpenAIResponse
import com.productscience.mockserver.model.Choice
import com.productscience.mockserver.model.Content
import com.productscience.mockserver.model.Usage
import io.ktor.http.*
import io.ktor.server.application.*
import io.ktor.server.response.*
import kotlinx.coroutines.delay

/**
 * Service for handling Server-Sent Events (SSE) streaming of OpenAI responses.
 */
class SSEService {
    private val objectMapper = ObjectMapper()
        .registerKotlinModule()
        .configure(com.fasterxml.jackson.databind.DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES, false)
        .setPropertyNamingStrategy(com.fasterxml.jackson.databind.PropertyNamingStrategies.SNAKE_CASE)

    /**
     * Parses the request body to determine if streaming is requested.
     *
     * @param requestBody The request body as a string
     * @return True if streaming is requested, false otherwise
     */
    fun isStreamingRequested(requestBody: String): Boolean {
        return try {
            val jsonNode = objectMapper.readTree(requestBody)
            jsonNode.has("stream") && jsonNode.get("stream").asBoolean()
        } catch (e: Exception) {
            false
        }
    }

    /**
     * Data class representing a streaming response chunk.
     */
    data class StreamingResponseChunk(
        val id: String,
        val `object`: String = "chat.completion.chunk",
        val created: Long,
        val model: String,
        val choices: List<StreamingChoice>,
        val usage: Usage? = null
    )

    /**
     * Data class representing a choice in a streaming response chunk.
     */
    data class StreamingChoice(
        val index: Int,
        val delta: Delta,
        val logprobs: StreamingLogprobs? = null,
        val finish_reason: String? = null
    )

    /**
     * Data class representing the delta in a streaming choice.
     */
    data class Delta(
        val content: String
    )

    /**
     * Data class representing logprobs in a streaming choice.
     */
    data class StreamingLogprobs(
        val content: List<Content>
    )

    /**
     * Converts a full OpenAI response to SSE format and sends it to the client.
     *
     * @param call The ApplicationCall to respond to
     * @param responseBody The full response body as a string
     * @param eventDelayMs The delay in milliseconds between sending each SSE event (default: 0)
     */
    suspend fun streamResponse(call: ApplicationCall, responseBody: String, eventDelayMs: Long = 0) {
        try {
            // Parse the response body
            val response = objectMapper.readValue(responseBody, OpenAIResponse::class.java)

            // Set up SSE response
            call.response.header(HttpHeaders.ContentType, ContentType.Text.EventStream.toString())
            call.response.header(HttpHeaders.CacheControl, "no-cache")
            call.response.header(HttpHeaders.Connection, "keep-alive")

            // Get the first choice and its logprobs
            val choice = response.choices.firstOrNull()
            val logprobs = choice?.logprobs

            // Use a single respondTextWriter call to keep the connection open
            call.respondTextWriter(ContentType.Text.EventStream) {
                if (choice != null && logprobs != null && logprobs.content.isNotEmpty()) {
                    // Stream each content token as a separate SSE event
                    for (i in logprobs.content.indices) {
                        // Add delay between events (skip delay for the first event)
                        if (i > 0 && eventDelayMs > 0) {
                            delay(eventDelayMs)
                        }

                        val isLast = i == logprobs.content.size - 1
                        val content = logprobs.content[i]

                        // Create a streaming response chunk
                        val chunk = StreamingResponseChunk(
                            id = response.id,
                            created = response.created,
                            model = response.model,
                            choices = listOf(
                                StreamingChoice(
                                    index = choice.index,
                                    delta = Delta(content = content.token),
                                    logprobs = StreamingLogprobs(content = listOf(content)),
                                    finish_reason = if (isLast) choice.finishReason else null
                                )
                            ),
                            usage = if (isLast) response.usage else null
                        )

                        // Convert to JSON and send as SSE event
                        val chunkJson = objectMapper.writeValueAsString(chunk)
                        write("data: $chunkJson\n\n")
                        flush()
                    }
                } else {
                    // If there are no logprobs or content, just send the full response as a single event
                    val chunkJson = objectMapper.writeValueAsString(response)
                    write("data: $chunkJson\n\n")
                }

                // Send the final [DONE] event
                write("data: [DONE]\n\n")
                flush()
            }
        } catch (e: Exception) {
            // If there's an error, respond with a simple error message
            call.respondTextWriter(ContentType.Text.EventStream) {
                write("data: {\"error\": \"${e.message}\"}\n\n")
                write("data: [DONE]\n\n")
                flush()
            }
        }
    }
}
