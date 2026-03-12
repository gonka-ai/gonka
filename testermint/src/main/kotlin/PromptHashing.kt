package com.productscience

import com.google.gson.Gson
import com.google.gson.GsonBuilder
import com.google.gson.JsonArray
import com.google.gson.JsonElement
import com.google.gson.JsonObject
import com.google.gson.JsonParser
import com.google.gson.JsonPrimitive
import java.math.BigDecimal
import java.security.MessageDigest
import java.util.TreeMap

private const val DEFAULT_MAX_TOKENS = 5000

data class PromptPayloadHash(
    val canonicalPayload: String,
    val promptHash: String
)

object PromptHashing {
    private val gson: Gson = GsonBuilder()
        .disableHtmlEscaping()
        .create()

    fun sha256Hex(input: String): String {
        val digest = MessageDigest.getInstance("SHA-256")
        return digest.digest(input.toByteArray()).joinToString("") { "%02x".format(it) }
    }

    fun canonicalizeJson(json: String): String {
        val element = JsonParser.parseString(json)
        val canonical = canonicalizeElement(element)
        // Match Go json.Encoder.Encode behavior (trailing newline).
        return gson.toJson(canonical) + "\n"
    }

    fun canonicalSha256(json: String): String = sha256Hex(canonicalizeJson(json))

    fun modifyRequestBody(requestJson: String, defaultSeed: Int): String {
        @Suppress("UNCHECKED_CAST")
        val requestMap = gson.fromJson(requestJson, MutableMap::class.java) as MutableMap<String, Any?>

        val originalLogprobs = getOriginalLogprobs(requestMap)
        if (originalLogprobs == null || !originalLogprobs) {
            requestMap["logprobs"] = true
        }

        val originalTopLogprobs = getOriginalTopLogprobs(requestMap)
        if (originalTopLogprobs == null || originalTopLogprobs < 5) {
            requestMap["top_logprobs"] = 5
        }

        val maxTokens = getMaxTokens(requestMap)
        requestMap["max_tokens"] = maxTokens
        requestMap["max_completion_tokens"] = maxTokens
        requestMap["skip_special_tokens"] = false

        if (!requestMap.containsKey("seed")) {
            requestMap["seed"] = defaultSeed
        }

        if (requestMap.containsKey("stream")) {
            val doStream = requestMap["stream"]
            if (doStream is Boolean && doStream) {
                val streamOptions = requestMap["stream_options"]
                if (!requestMap.containsKey("stream_options")) {
                    requestMap["stream_options"] = mutableMapOf("include_usage" to true)
                } else if (streamOptions is MutableMap<*, *>) {
                    @Suppress("UNCHECKED_CAST")
                    val streamOptionsMap = streamOptions as MutableMap<String, Any?>
                    streamOptionsMap["include_usage"] = true
                } else {
                    requestMap["stream_options"] = mutableMapOf("include_usage" to true)
                }
            }
        }

        return gson.toJson(requestMap)
    }

    fun computeModifiedPromptHash(requestJson: String, defaultSeed: Long = 0): String {
        return computeModifiedPromptPayloadAndHash(requestJson, defaultSeed).promptHash
    }

    fun computeModifiedPromptPayloadAndHash(requestJson: String, defaultSeed: Long = 0): PromptPayloadHash {
        val modifiedJson = modifyRequestBody(requestJson, defaultSeed.toInt())
        val canonicalPayload = canonicalizeJson(modifiedJson)
        return PromptPayloadHash(
            canonicalPayload = canonicalPayload,
            promptHash = sha256Hex(canonicalPayload)
        )
    }

    private fun canonicalizeElement(element: JsonElement): JsonElement {
        return when {
            element.isJsonObject -> {
                val obj = element.asJsonObject
                val sortedMap = TreeMap<String, JsonElement>()
                for (entry in obj.entrySet()) {
                    sortedMap[entry.key] = canonicalizeElement(entry.value)
                }

                val result = JsonObject()
                for ((key, value) in sortedMap) {
                    result.add(key, value)
                }
                result
            }

            element.isJsonArray -> {
                val arr = element.asJsonArray
                val result = JsonArray()
                for (item in arr) {
                    result.add(canonicalizeElement(item))
                }
                result
            }

            element.isJsonPrimitive -> canonicalizePrimitive(element.asJsonPrimitive)

            else -> element
        }
    }

    private fun canonicalizePrimitive(primitive: JsonPrimitive): JsonElement {
        if (!primitive.isNumber) {
            return primitive
        }

        val value: BigDecimal = primitive.asBigDecimal
        val normalized = value.stripTrailingZeros()
        return if (normalized.scale() <= 0) {
            val intNumber: Number = normalized.toBigInteger()
            JsonPrimitive(intNumber)
        } else {
            val decimalNumber: Number = normalized
            JsonPrimitive(decimalNumber)
        }
    }

    private fun getOriginalLogprobs(requestMap: Map<String, Any?>): Boolean? {
        if (!requestMap.containsKey("logprobs")) {
            return null
        }

        val value = requestMap["logprobs"] ?: return null
        return if (value is Boolean) value else true
    }

    private fun getOriginalTopLogprobs(requestMap: Map<String, Any?>): Int? {
        if (!requestMap.containsKey("top_logprobs")) {
            return null
        }

        val value = requestMap["top_logprobs"] ?: return null
        return when (value) {
            is Int -> value
            is Boolean -> if (value) 1 else 0
            else -> null
        }
    }

    private fun getMaxTokens(requestMap: Map<String, Any?>): Int {
        parseTokenLimit(requestMap["max_tokens"])?.let { return it }
        parseTokenLimit(requestMap["max_completion_tokens"])?.let { return it }
        return DEFAULT_MAX_TOKENS
    }

    private fun parseTokenLimit(value: Any?): Int? {
        return when (value) {
            is Double -> value.toInt()
            is Int -> value
            else -> null
        }
    }
}
