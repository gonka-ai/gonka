package com.productscience.analyzer

import com.google.gson.Gson
import com.google.gson.JsonObject
import java.io.File
import java.io.FileReader

/**
 * Class that encapsulates a log file and provides methods to analyze it
 */
class LogAnalyzer(private val logFile: File) {
    private val database = LogDatabase()
    private var logFormat: JsonObject? = null

    init {
        // Load the log file into the database
        database.loadLogFile(logFile)
    }

    /**
     * Load log format from a JSON file
     *
     * @param formatFile The JSON file containing the log format
     */
    fun loadFormatFromFile(formatFile: File) {
        val gson = Gson()
        logFormat = gson.fromJson(FileReader(formatFile), JsonObject::class.java)
    }

    /**
     * Load log format from a JSON string
     *
     * @param formatJson The JSON string containing the log format
     */
    fun loadFormatFromString(formatJson: String) {
        val gson = Gson()
        logFormat = gson.fromJson(formatJson, JsonObject::class.java)
    }

    /**
     * Get the total number of lines in the log file
     *
     * @return The total number of lines
     */
    fun getTotalLines(): Int {
        return database.getTotalLines()
    }

    /**
     * Get the total number of error entries in the log file
     *
     * @return The total number of errors
     */
    fun getTotalErrors(): Int {
        return database.getErrorCount()
    }

    /**
     * Get the total number of warning entries in the log file
     *
     * @return The total number of warnings
     */
    fun getTotalWarnings(): Int {
        return database.getWarnCount()
    }

    /**
     * Get basic statistics about the log file
     *
     * @return A map containing basic statistics
     */
    fun getBasicStats(): Map<String, Int> {
        return mapOf(
            "totalLines" to getTotalLines(),
            "totalErrors" to getTotalErrors(),
            "totalWarns" to getTotalWarnings()
        )
    }

    /**
     * Print basic statistics about the log file
     */
    fun printBasicStats() {
        val stats = getBasicStats()
        println("Total lines: ${stats["totalLines"]}")
        println("Total errors: ${stats["totalErrors"]}")
        println("Total warns: ${stats["totalWarns"]}")
    }

    /**
     * Execute a custom SQL query on the log database
     *
     * @param query The SQL query to execute
     * @return The query result
     */
    fun executeQuery(query: String): List<Map<String, Any?>> {
        return database.executeQuery(query)
    }

    /**
     * Get the number of log entries with a specific level
     *
     * @param level The log level to count
     * @return The number of log entries with the specified level
     */
    fun getCountByLevel(level: String): Int {
        return database.getCountByLevel(level)
    }

    /**
     * Get the number of log entries for a specific service
     *
     * @param service The service name
     * @return The number of log entries for the specified service
     */
    fun getCountByService(service: String): Int {
        val result = database.executeQuery("SELECT COUNT(*) as count FROM LogEntries WHERE service = '$service'")
        val countObj = result.firstOrNull()?.get("count")
        return when (countObj) {
            is Int -> countObj
            is Long -> countObj.toInt()
            else -> 0
        }
    }

    /**
     * Get the SQL schema of the database
     *
     * @return A list of maps containing table schema information
     */
    fun getDatabaseSchema(): List<Map<String, Any?>> {
        return database.executeQuery("SELECT sql FROM sqlite_master WHERE type='table' AND name='LogEntries'")
    }
}
