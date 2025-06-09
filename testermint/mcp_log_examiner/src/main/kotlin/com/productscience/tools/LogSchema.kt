package com.productscience.tools

import com.productscience.LogAnalyzerSession
import io.modelcontextprotocol.kotlin.sdk.CallToolResult
import io.modelcontextprotocol.kotlin.sdk.TextContent
import io.modelcontextprotocol.kotlin.sdk.Tool
import io.modelcontextprotocol.kotlin.sdk.server.RegisteredTool

fun getLogSchema(session: LogAnalyzerSession) = RegisteredTool(
    tool = Tool(
        name = "log-schema",
        description = "Returns the schema of the currently loaded logfile",
        inputSchema = Tool.Input()
    )
) { _ ->
    val schemaText = if (!session.isLogLoaded()) {
        "No logfile loaded, call log-load first"
    } else {
        session.getCurrentAnalyzer().getDatabaseSchema().joinToString("\n") { it.toString() }
    }

    CallToolResult(content = listOf(TextContent(schemaText)))
}