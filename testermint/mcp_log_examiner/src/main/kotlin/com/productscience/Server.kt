package com.productscience

import com.productscience.prompts.analyzeLogPrompt
import com.productscience.resources.getGuides
import com.productscience.resources.getSqlQueryResources
import com.productscience.tools.getLoadLog
import com.productscience.tools.getLogQuery
import com.productscience.tools.getLogSchema
import io.modelcontextprotocol.kotlin.sdk.Implementation
import io.modelcontextprotocol.kotlin.sdk.ServerCapabilities
import io.modelcontextprotocol.kotlin.sdk.server.Server
import io.modelcontextprotocol.kotlin.sdk.server.ServerOptions

fun getServer(): Pair<Server, LogAnalyzerSession> {
    val server = Server(
        serverInfo = Implementation(
            name = "log-examiner",
            version = "1.0.0"
        ),
        options = ServerOptions(
            capabilities = ServerCapabilities(
                resources = ServerCapabilities.Resources(
                    subscribe = true,
                    listChanged = true
                ),
                tools = ServerCapabilities.Tools(
                    true
                ),
                prompts = ServerCapabilities.Prompts(
                    listChanged = true
                )
            )
        )
    )

    val session = LogAnalyzerSession()
    server.addResources(getSqlQueryResources())
    server.addResources(getGuides())
    server.addPrompts(listOf(analyzeLogPrompt))
    server.addTools(listOf(getLoadLog(session), getLogSchema(session), getLogQuery(session)))
    return server to session
}
