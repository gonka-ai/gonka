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
import java.io.File

/**
 * Load a log file into the LogDatabase
 *
 * @param logfile The full path to the log file to load
 * @return The number of entries loaded
 */
@JsonSchema
@Serializable
data class LoadLogRequest(
    val logfile: String
)

fun getLoadLog(session: LogAnalyzerSession) = RegisteredTool(
    tool = Tool(
        "load-log", "Load logfile to be analyzed", Tool.Input(
            properties = getSchema<LoadLogRequest>()
        )
    )
) { request ->
    val loadLogRequest = Json.decodeFromJsonElement<LoadLogRequest>(request.arguments)
    val file = File(loadLogRequest.logfile)

    if (!file.exists()) {
        CallToolResult(content = listOf(TextContent("File not found: ${loadLogRequest.logfile}")))
    } else {
        session.loadLog(file)
        CallToolResult(
            content = listOf(
                TextContent(
                    "Loaded ${loadLogRequest.logfile} with ${
                        session.getCurrentAnalyzer().getTotalLines()
                    } entries"
                )
            )
        )
    }

}