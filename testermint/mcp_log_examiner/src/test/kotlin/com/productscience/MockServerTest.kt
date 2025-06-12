package com.productscience

import com.productscience.tools.formatResultAsText
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import java.io.File

/**
 * Test the server component by directly calling the tool handler functions
 */
class MockServerTest {

    @BeforeEach
    fun setup() {
        // Reset the logAnalyzer before each test
        println("[DEBUG_LOG] Reset logAnalyzer to null")
    }

    private fun getTestLogFile(): File {
        val classLoader = javaClass.classLoader
        return File(
            classLoader.getResource("testermint.log")?.file ?: throw IllegalStateException("Test log file not found")
        )
    }

    @Test
    fun `should load log file and create analyzer`() {
        // Set up the server with tools
        val (_, session) = getServer()

        // Get the test log file
        val logFile = getTestLogFile()

        assertThat(session.isLogLoaded()).isFalse()

        // Load the log file
        session.loadLog(logFile)

        // Verify that the log file was loaded
        assertThat(session.isLogLoaded()).isTrue()
        assertThat(session.getCurrentAnalyzer().getTotalLines()).isGreaterThan(0)

        // Get basic statistics
        val stats = session.getCurrentAnalyzer().getBasicStats()

        // Verify that we have some data
        assertThat(stats["totalLines"]).isGreaterThan(0)
    }

    @Test
    fun `should retrieve database schema with correct structure`() {
        // Set up the server with tools
        val (_, session) = getServer()

        // Load the log file
        session.loadLog(getTestLogFile())

        // Call the log-schema tool handler directly
        val schema = session.getCurrentAnalyzer().getDatabaseSchema()

        // Verify that we got a schema
        assertThat(schema).isNotNull()
        assertThat(schema).isNotEmpty()

        // Verify that the schema contains information about the LogEntries table
        val schemaString = schema.firstOrNull()?.get("sql")?.toString() ?: ""
        assertThat(schemaString).contains("LogEntries")
        assertThat(schemaString).contains("timestamp")
        assertThat(schemaString).contains("level")
        assertThat(schemaString).contains("message")
    }

    @Test
    fun `should execute SQL query and return formatted results`() {
        // Set up the server with tools
        val (_, session) = getServer()

        // Load the log file
        session.loadLog(getTestLogFile())

        // Call the log-query tool handler directly
        val queryResult = session.getCurrentAnalyzer().executeQuery("SELECT COUNT(*) as count FROM LogEntries")

        // Verify that we got results
        assertThat(queryResult).isNotNull()
        assertThat(queryResult).isNotEmpty()

        // Format the results as text
        val formattedResult = formatResultAsText(queryResult)

        // Verify that the results contain the count
        assertThat(formattedResult).contains("count")
    }
}
