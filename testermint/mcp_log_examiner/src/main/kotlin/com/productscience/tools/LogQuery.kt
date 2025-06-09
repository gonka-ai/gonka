package com.productscience.tools

import com.productscience.LogAnalyzerSession
import com.productscience.getSchema
import io.modelcontextprotocol.kotlin.sdk.CallToolResult
import io.modelcontextprotocol.kotlin.sdk.TextContent
import io.modelcontextprotocol.kotlin.sdk.Tool
import io.modelcontextprotocol.kotlin.sdk.server.RegisteredTool
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.decodeFromJsonElement
import sh.ondr.koja.JsonSchema

/**
 * Query the currently loaded logfile using SQL
 *
 * @param query The SQL query to execute
 * @param format The output format (JSON or text)
 * @param limit The maximum number of records to return (default 50). Increase this sparingly, tokens are expensive!
 */
@JsonSchema
@Serializable
data class LogQueryRequest(
    val query: String,
    val format: String = "json", // Default to JSON format
    val limit: Int = 50 // Default to 50 records
)

fun getLogQuery(session: LogAnalyzerSession) = RegisteredTool(
    tool = Tool(
        name = "log-query",
        description = "Queries the currently loaded logfile using SQL",
        inputSchema = Tool.Input(
            properties = getSchema<LogQueryRequest>()
        )
    )
) { request ->
    val logQueryRequest = Json.decodeFromJsonElement<LogQueryRequest>(request.arguments)
    val query = logQueryRequest.query
    val format = logQueryRequest.format.lowercase()
    val limit = logQueryRequest.limit

    val result = if (session.isLogLoaded() && query.isNotEmpty()) {
        try {
            val queryResult = session.getCurrentAnalyzer().executeQuery(query)

            // Check if the number of records exceeds the limit
            if (queryResult.size > limit) {
                "Total matching records: ${queryResult.size}. This exceeds the limit of $limit records. Refine the query to be narrower in scope. If ABSOLUTELY needed, increase the limit. Tokens are expensive."
            } else {
                when (format) {
                    "text" -> formatResultAsText(queryResult)
                    else -> queryResult.joinToString("\n") { it.toString() } // Default JSON format
                }
            }
        } catch (e: Exception) {
            "Error executing query: ${e.message}"
        }
    } else if (!session.isLogLoaded()) {
        "No logfile loaded, call log-load first"
    } else {
        "Please provide an SQL query (e.g., SELECT * FROM LogEntries)"
    }

    CallToolResult(content = listOf(TextContent(result)))
}


/**
 * Format query results as human-readable text
 *
 * @param results The query results to format
 * @return Formatted text output
 */
fun formatResultAsText(results: List<Map<String, Any?>>): String {
    if (results.isEmpty()) return "No results found"

    val sb = StringBuilder()

    // Get all column names from the first result
    val columns = results.first().keys

    // Calculate column widths for proper alignment
    val columnWidths = mutableMapOf<String, Int>()
    columns.forEach { column ->
        // Start with the column name length
        var maxWidth = column.length

        // Check all values in this column
        results.forEach { row ->
            val value = formatValue(row[column])
            // For multi-line values, find the longest line
            val lines = value.split("\n")
            lines.forEach { line ->
                if (line.length > maxWidth) {
                    maxWidth = line.length
                }
            }
        }

        columnWidths[column] = maxWidth
    }

    // Build header row
    columns.forEach { column ->
        sb.append(column.padEnd(columnWidths[column]!! + 2))
    }
    sb.append("\n")

    // Add separator line
    columns.forEach { column ->
        sb.append("-".repeat(columnWidths[column]!!)).append("  ")
    }
    sb.append("\n")

    // Add data rows
    results.forEach { row ->
        // For each row, we might need multiple lines if any column has multi-line content
        val rowValues = columns.associateWith { column -> formatValue(row[column]).split("\n") }
        val maxLines = rowValues.values.maxOfOrNull { it.size } ?: 1

        // Process each line of the row
        for (lineIndex in 0 until maxLines) {
            columns.forEach { column ->
                val lines = rowValues[column] ?: listOf("")
                // If this column has a value for this line, display it, otherwise show empty
                val lineValue = if (lineIndex < lines.size) lines[lineIndex] else ""
                sb.append(lineValue.padEnd(columnWidths[column]!! + 2))
            }
            sb.append("\n")
        }
    }

    return sb.toString()
}

/**
 * Format a value for text output, making timestamps human-readable
 *
 * @param value The value to format
 * @return Formatted value as string
 */
fun formatValue(value: Any?): String {
    return when (value) {
        null -> "NULL"
        is java.sql.Time -> value.toString() // Format SQL time as string
        is java.time.LocalTime -> value.toString() // Format LocalTime as string
        else -> value.toString()
    }
}
