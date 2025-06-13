package com.productscience.mockserver.model

import com.fasterxml.jackson.annotation.JsonProperty

/**
 * Data classes representing the OpenAI API response structure.
 * These are used for serialization/deserialization of OpenAI API responses.
 */

data class OpenAIResponse(
    val choices: List<Choice>,
    val created: Long,
    val id: String,
    val model: String,
    val `object`: String,
    val usage: Usage,
) {
    fun withMissingLogit(): OpenAIResponse {
        return this.copy(
            choices = listOf(
                this.choices.first().copy(
                    logprobs = this.choices.first().logprobs?.copy(
                        content = this.choices.first().logprobs?.content?.drop(1) ?: listOf()
                    )
                )
            )
        )
    }

    fun withResponse(response: String): OpenAIResponse {
        return this.copy(
            choices = listOf(
                this.choices.first().copy(message = ResponseMessage(response, "system", listOf()))
            )
        )
    }
}

data class Choice(
    val finishReason: String?,
    val index: Int,
    val logprobs: Logprobs?,
    val message: ResponseMessage,
    val stopReason: Any?,
)

data class Logprobs(
    val content: List<Content>,
)

data class Content(
    val bytes: List<Int>,
    val logprob: Double,
    val token: String,
    @JsonProperty("top_logprobs")
    val topLogprobs: List<TopLogprob>,
)

data class TopLogprob(
    val bytes: List<Int>,
    val logprob: Double,
    val token: String,
)

data class ResponseMessage(
    val content: String,
    val role: String,
    val toolCalls: List<Any>?,
)

data class Usage(
    val completionTokens: Int,
    val promptTokens: Int,
    val totalTokens: Int,
)