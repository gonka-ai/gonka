package com.productscience

import com.productscience.tools.formatResultAsText
import com.productscience.tools.formatValue
import org.junit.jupiter.api.Test
import org.assertj.core.api.Assertions.assertThat
import java.time.LocalTime
import java.sql.Time

class FormatTest {

    @Test
    fun `should format different value types correctly`() {
        // Test null value
        assertThat(formatValue(null)).isEqualTo("NULL")

        // Test LocalTime value
        val localTime = LocalTime.of(12, 34, 56)
        assertThat(formatValue(localTime)).isEqualTo("12:34:56")

        // Test SQL Time value
        val sqlTime = Time.valueOf("12:34:56")
        assertThat(formatValue(sqlTime)).isEqualTo("12:34:56")

        // Test other values
        assertThat(formatValue(42)).isEqualTo("42")
        assertThat(formatValue(true)).isEqualTo("true")
        assertThat(formatValue("test")).isEqualTo("test")
    }

    @Test
    fun `should format query results as text table`() {
        // Create a sample result with different types of values
        val results = listOf(
            mapOf(
                "id" to 1,
                "timestamp" to LocalTime.of(12, 34, 56),
                "level" to "INFO",
                "message" to "Test message 1"
            ),
            mapOf(
                "id" to 2,
                "timestamp" to LocalTime.of(12, 35, 0),
                "level" to "ERROR",
                "message" to "Test message 2"
            ),
            mapOf(
                "id" to 3,
                "timestamp" to LocalTime.of(12, 36, 30),
                "level" to "WARN",
                "message" to "Test message 3"
            )
        )

        // Format the results as text
        val formattedText = formatResultAsText(results)

        // Verify the output contains expected elements
        assertThat(formattedText).contains("id")
        assertThat(formattedText).contains("timestamp")
        assertThat(formattedText).contains("level")
        assertThat(formattedText).contains("message")

        // Verify the output contains the data
        assertThat(formattedText).contains("1")
        assertThat(formattedText).contains("2")
        assertThat(formattedText).contains("3")
        assertThat(formattedText).contains("12:34:56")
        assertThat(formattedText).contains("INFO")
        assertThat(formattedText).contains("ERROR")
        assertThat(formattedText).contains("WARN")
        assertThat(formattedText).contains("Test message 1")
        assertThat(formattedText).contains("Test message 2")
        assertThat(formattedText).contains("Test message 3")
    }

    @Test
    fun `should handle empty results`() {
        // Test with empty results
        val emptyResults = emptyList<Map<String, Any?>>()
        assertThat(formatResultAsText(emptyResults)).isEqualTo("No results found")
    }
}
