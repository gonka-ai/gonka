package com.productscience.data

data class RequestModel(
    val model: String,
    val messages: List<Message>,
    val frequencyPenalty: Int,
    val logitBias: Any?,
    val logprobs: Boolean,
    val topLogprobs: Int,
    val maxTokens: Int,
    val n: Int,
    val presencePenalty: Int,
    val responseFormat: ResponseFormat,
    val seed: Int,
    val serviceTier: Any?,
    val stop: String,
    val stream: Boolean,
    val streamOptions: Any?,
    val temperature: Int,
    val topP: Int,
    val tools: List<Tool>,
    val toolChoice: String,
    val parallelToolCalls: Boolean,
    val user: String,
    val functionCall: String,
    val functions: List<Function>
)

data class Message(
    val content: String,
    val role: String,
    val name: String
)

data class ResponseFormat(
    val type: String
)

data class Tool(
    val type: String,
    val function: FunctionDetails
)

data class FunctionDetails(
    val name: String,
    val description: String,
    val parameters: Map<String, Any>,
    val strict: Boolean
)

data class Function(
    val name: String,
    val description: String,
    val parameters: Map<String, Any>
)

// Response

data class OpenAIResponse(
    val choices: List<Choice>,
    val created: Long,
    val id: String,
    val model: String,
    val `object`: String,
    val usage: Usage
)

data class Choice(
    val finishReason: String,
    val index: Int,
    val logprobs: Logprobs?,
    val message: ResponseMessage,
    val stopReason: Any?
)

data class Logprobs(
    val content: List<Content>
)

data class Content(
    val bytes: List<Int>,
    val logprob: Double,
    val token: String,
    val topLogprobs: List<TopLogprob>
)

data class TopLogprob(
    val bytes: List<Int>,
    val logprob: Double,
    val token: String
)

data class ResponseMessage(
    val content: String,
    val role: String,
    val toolCalls: List<Any>
)

data class Usage(
    val completionTokens: Int,
    val promptTokens: Int,
    val totalTokens: Int
)
