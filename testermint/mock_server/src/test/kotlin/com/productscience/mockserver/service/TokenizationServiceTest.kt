package com.productscience.mockserver.service

import com.productscience.mockserver.routes.TokenizationResponse
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Assertions.*

class TokenizationServiceTest {

    private val tokenizationService = TokenizationService()

    @Test
    fun `test tokenize returns correct response structure`() {
        // Given
        val model = "Qwen/Qwen2.5-7B-Instruct"
        val prompt = "This is a prompt"

        // When
        val response = tokenizationService.tokenize(model, prompt)

        // Then
        assertNotNull(response)
        assertEquals(4, response.count) // "This", "is", "a", "prompt" = 4 tokens
        assertEquals(32768, response.maxModelLen) // From the predefined model max lengths
        assertEquals(4, response.tokens.size)
        assertTrue(response.tokens.all { it > 0 }) // All token IDs should be positive
    }

    @Test
    fun `test tokenize with unknown model uses default max length`() {
        // Given
        val model = "unknown-model"
        val prompt = "This is a prompt"

        // When
        val response = tokenizationService.tokenize(model, prompt)

        // Then
        assertNotNull(response)
        assertEquals(4, response.count)
        assertEquals(4096, response.maxModelLen) // Default max length
        assertEquals(4, response.tokens.size)
    }

    @Test
    fun `test tokenize with empty prompt returns empty tokens list`() {
        // Given
        val model = "gpt-3.5-turbo"
        val prompt = ""

        // When
        val response = tokenizationService.tokenize(model, prompt)

        // Then
        assertNotNull(response)
        assertEquals(0, response.count)
        assertEquals(4096, response.maxModelLen)
        assertTrue(response.tokens.isEmpty())
    }
}