package com.productscience

import com.productscience.tools.formatResultAsText
import org.junit.jupiter.api.Test
import org.assertj.core.api.Assertions.assertThat

class MainTest {

    @Test
    fun `formatResultAsText should handle multi-line values correctly`() {
        // Create test data with multi-line values
        val results = listOf(
            mapOf(
                "id" to 1,
                "level" to "ERROR",
                "message" to "This is a multi-line message\nwith a second line\nand a third line"
            ),
            mapOf(
                "id" to 2,
                "level" to "INFO",
                "message" to "This is a single line message"
            )
        )

        // Format the results as text
        val formattedText = formatResultAsText(results)

        // Verify that the output is formatted correctly
        // The message column width should be based on the longest line, not the total length
        println("[DEBUG_LOG] Formatted text output:\n$formattedText")

        // Check that the output contains all the expected lines
        assertThat(formattedText).contains("id  level  message")
        assertThat(formattedText).contains("1   ERROR  This is a multi-line message")
        assertThat(formattedText).contains("          with a second line")
        assertThat(formattedText).contains("          and a third line")
        assertThat(formattedText).contains("2   INFO   This is a single line message")

        // Verify that the column widths are correct
        val lines = formattedText.split("\n")
        val headerLine = lines.first()
        val messageColumnStart = headerLine.indexOf("message")
        
        // Check that all lines in the multi-line message start at the same position
        val lineWithMultilineStart = lines.first { it.contains("This is a multi-line message") }
        val secondLineOfMultiline = lines.first { it.contains("with a second line") }
        val thirdLineOfMultiline = lines.first { it.contains("and a third line") }
        
        val multilineMessageStart = lineWithMultilineStart.indexOf("This")
        val secondLineStart = secondLineOfMultiline.indexOf("with")
        val thirdLineStart = thirdLineOfMultiline.indexOf("and")
        
        // All lines of the multi-line message should be aligned
        assertThat(multilineMessageStart).isEqualTo(messageColumnStart)
        assertThat(secondLineStart).isEqualTo(messageColumnStart)
        assertThat(thirdLineStart).isEqualTo(messageColumnStart)
    }
}