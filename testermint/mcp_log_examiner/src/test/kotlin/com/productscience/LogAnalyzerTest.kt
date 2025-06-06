package com.productscience

import com.productscience.analyzer.LogAnalyzer
import org.junit.jupiter.api.Test
import org.assertj.core.api.Assertions.assertThat
import java.io.File

class LogAnalyzerTest {

    private fun getTestLogFile(): File {
        val classLoader = javaClass.classLoader
        return File(classLoader.getResource("testermint.log")?.file ?: throw IllegalStateException("Test log file not found"))
    }

    @Test
    fun `should get basic statistics from log file`() {
        // Create a log analyzer
        val analyzer = LogAnalyzer(getTestLogFile())

        // Get basic statistics
        val stats = analyzer.getBasicStats()

        // Assert that we have some data
        assertThat(stats["totalLines"]).isGreaterThan(0)
    }

    @Test
    fun `should have consistent counts between methods`() {
        // Create a log analyzer
        val analyzer = LogAnalyzer(getTestLogFile())

        // Get basic statistics
        val stats = analyzer.getBasicStats()

        // Get counts using different methods
        val totalLines = analyzer.getTotalLines()
        val errorCount = analyzer.getTotalErrors()
        val warnCount = analyzer.getTotalWarnings()

        // Verify that the counts are consistent
        assertThat(totalLines).isEqualTo(stats["totalLines"])
        assertThat(errorCount).isEqualTo(stats["totalErrors"])
        assertThat(warnCount).isEqualTo(stats["totalWarns"])
    }

    @Test
    fun `should count entries by service`() {
        // Create a log analyzer
        val analyzer = LogAnalyzer(getTestLogFile())

        // Test querying by service
        val nodeServiceCount = analyzer.getCountByService("node")
        val testServiceCount = analyzer.getCountByService("test")
        val dapiServiceCount = analyzer.getCountByService("dapi")

        // Verify that we have entries for each service
        assertThat(nodeServiceCount).isGreaterThan(0)
        assertThat(testServiceCount).isGreaterThan(0)
        assertThat(dapiServiceCount).isGreaterThan(0)
    }

    @Test
    fun `should print basic statistics`() {
        // Create a log analyzer
        val analyzer = LogAnalyzer(getTestLogFile())

        // Print basic statistics - this is just a visual test
        println("Basic statistics for testermint.log:")
        analyzer.printBasicStats()

        // Since this is just printing, we'll assert that the analyzer exists
        assertThat(analyzer).isNotNull()
    }

    @Test
    fun `should retrieve database schema with correct structure`() {
        // Create a log analyzer
        val analyzer = LogAnalyzer(getTestLogFile())

        // Get the database schema
        val schema = analyzer.getDatabaseSchema()

        // Verify that we got a schema
        assertThat(schema).isNotEmpty()

        // Verify that the schema contains information about the LogEntries table
        val schemaString = schema.firstOrNull()?.get("sql")?.toString() ?: ""
        assertThat(schemaString).contains("LogEntries")
        assertThat(schemaString).contains("timestamp")
        assertThat(schemaString).contains("level")
        assertThat(schemaString).contains("message")
    }
}
