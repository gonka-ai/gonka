package com.productscience.analyzer

import java.io.File
import java.time.LocalTime
import java.time.format.DateTimeFormatter

/**
 * Class representing a parsed log entry
 */
data class LogEntry(
    val timestamp: LocalTime,
    val level: String,
    val context: String?,
    val service: String,
    val pair: String,
    val message: String,
    val subsystem: String? = null
)

/**
 * Class for parsing log files according to the Testermint log format
 */
class LogParser {
    companion object {
        // Regex pattern from the JSON format
        private val LOG_PATTERN = Regex(
            "^(?<timestamp>\\d{2}:\\d{2}:\\d{2}\\.\\d{3})\\s+(?<level>\\w+)\\s*-\\s*(?:\\[(?<context>[^\\]]+)\\]\\s*)?(?<service>[\\w.-]+):pair=(?:\\/)?(?<pair>[^\\s]+)\\s+\"(?<message>.*?)\"(?:\\s+\\w+=\\w+)?$"
        )
        private val SUBSYSTEM_PATTERN = Regex("(?:\\[36msubsystem=\\[0m|subsystem=)([^\\s\"]+)")
        private val ANSI_PATTERN = Regex("\\x1B\\[[0-9;]*[a-zA-Z]|\\[[0-9]+m")
        private val TIME_FORMATTER = DateTimeFormatter.ofPattern("HH:mm:ss.SSS")
    }

    /**
     * Strip ANSI color codes from a string
     *
     * @param input The string to strip ANSI codes from
     * @return The string without ANSI codes
     */
    private fun stripAnsiCodes(input: String): String {
        return ANSI_PATTERN.replace(input, "")
    }

    /**
     * Parse a log file and return a list of LogEntry objects
     *
     * @param file The log file to parse
     * @return List of parsed log entries
     */
    fun parseLogFile(file: File): List<LogEntry> {
        val result = mutableListOf<LogEntry>()
        var currentEntry: LogEntry? = null

        for (line in file.readLines()) {
            val parsedLine = parseLine(line)

            if (parsedLine != null) {
                // If we have a current entry, add it to the result before moving on
                if (currentEntry != null) {
                    result.add(currentEntry!!)
                }
                currentEntry = parsedLine
            } else if (currentEntry != null) {
                // If the line doesn't match the pattern and we have a current entry,
                // append the line to the current entry's message
                val entry = currentEntry!!
                currentEntry = entry.copy(
                    message = entry.message + "\n" + stripAnsiCodes(line)
                )
            }
            // If parsedLine is null and currentEntry is null, we just skip this line
        }

        // Don't forget to add the last entry if it exists
        if (currentEntry != null) {
            result.add(currentEntry!!)
        }

        return result
    }

    /**
     * Parse a single log line and return a LogEntry object
     *
     * @param line The log line to parse
     * @return LogEntry object or null if the line doesn't match the pattern
     */
    fun parseLine(line: String): LogEntry? {
        // Strip ANSI codes from the log line
        val cleanLine = stripAnsiCodes(line)

        val matchResult = LOG_PATTERN.find(cleanLine) ?: return null

        val timestamp = LocalTime.parse(
            matchResult.groups["timestamp"]?.value ?: return null,
            TIME_FORMATTER
        )
        val level = matchResult.groups["level"]?.value ?: return null
        val context = matchResult.groups["context"]?.value
        val service = matchResult.groups["service"]?.value ?: return null
        val pair = matchResult.groups["pair"]?.value ?: return null
        val message = matchResult.groups["message"]?.value ?: return null

        // Extract subsystem from message if present
        // We need to strip ANSI codes from the message as well to ensure proper subsystem extraction
        // Remove the trailing quote if present
        val messageWithoutTrailingQuote = if (message.endsWith("\"")) message.substring(0, message.length - 1) else message
        val cleanMessage = stripAnsiCodes(messageWithoutTrailingQuote)
        val subsystemMatch = SUBSYSTEM_PATTERN.find(cleanMessage)
        val subsystem = subsystemMatch?.groupValues?.get(1)

        return LogEntry(
            timestamp = timestamp,
            level = level,
            context = context,
            service = service,
            pair = pair,
            message = cleanMessage,  // Use the clean message without ANSI codes
            subsystem = subsystem
        )
    }
}
