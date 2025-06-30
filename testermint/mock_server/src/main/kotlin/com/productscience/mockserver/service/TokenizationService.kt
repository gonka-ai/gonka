package com.productscience.mockserver.service

import com.knuddels.jtokkit.Encodings
import com.knuddels.jtokkit.api.EncodingType
import com.productscience.mockserver.routes.TokenizationResponse
import org.slf4j.LoggerFactory
import java.util.concurrent.ConcurrentHashMap

/**
 * Service for tokenizing text using the jtokkit library.
 * This implementation uses the jtokkit library to provide accurate tokenization
 * for various models.
 */
class TokenizationService {
    private val logger = LoggerFactory.getLogger(this::class.java)

    // Initialize the registry with all available encodings
    private val registry = Encodings.newDefaultEncodingRegistry()

    // Map model names to encoding types
    private val modelEncodingMap = mapOf(
        "gpt-3.5-turbo" to EncodingType.CL100K_BASE,
        "gpt-4" to EncodingType.CL100K_BASE,
        "gpt-4-turbo" to EncodingType.CL100K_BASE,
        "claude-3-opus" to EncodingType.CL100K_BASE,
        "claude-3-sonnet" to EncodingType.CL100K_BASE,
        "claude-3-haiku" to EncodingType.CL100K_BASE,
        "Qwen/Qwen2.5-7B-Instruct" to EncodingType.CL100K_BASE
    )

    // Cache of model max lengths
    private val modelMaxLengths = ConcurrentHashMap<String, Int>().apply {
        // Default max lengths for common models
        put("Qwen/Qwen2.5-7B-Instruct", 32768)
        put("gpt-3.5-turbo", 4096)
        put("gpt-4", 8192)
        put("gpt-4-turbo", 128000)
        put("claude-3-opus", 200000)
        put("claude-3-sonnet", 200000)
        put("claude-3-haiku", 200000)
        // Add more models as needed
    }

    /**
     * Tokenizes the provided prompt for the specified model using the jtokkit library.
     * 
     * @param model The model to tokenize for
     * @param prompt The prompt to tokenize
     * @return TokenizationResponse containing the token count, max model length, and token IDs
     */
    fun tokenize(model: String, prompt: String): TokenizationResponse {
        logger.info("Tokenizing prompt for model: $model")

        // Get the max model length, defaulting to 4096 if not found
        val maxModelLen = modelMaxLengths.getOrDefault(model, 4096)

        // Handle empty prompt
        if (prompt.isBlank()) {
            return TokenizationResponse(
                count = 0,
                maxModelLen = maxModelLen,
                tokens = emptyList()
            )
        }

        // Get the appropriate encoding for the model
        val encodingType = modelEncodingMap[model] ?: EncodingType.CL100K_BASE
        val encoding = registry.getEncoding(encodingType)

        // If model is not in our map, log a warning
        if (!modelEncodingMap.containsKey(model)) {
            logger.warn("Model $model not recognized, using default encoding (cl100k_base)")
        }

        // Encode the prompt to get token IDs
        val tokens = encoding.encode(prompt)

        return TokenizationResponse(
            count = tokens.size,
            maxModelLen = maxModelLen,
            tokens = tokens
        )
    }
}
