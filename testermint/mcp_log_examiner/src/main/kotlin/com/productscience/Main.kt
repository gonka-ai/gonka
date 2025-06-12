package com.productscience

import io.modelcontextprotocol.kotlin.sdk.server.Server
import io.modelcontextprotocol.kotlin.sdk.server.StdioServerTransport
import kotlinx.coroutines.Job
import kotlinx.coroutines.runBlocking
import kotlinx.io.asSink
import kotlinx.io.asSource
import kotlinx.io.buffered
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import sh.ondr.koja.JsonSchema
import sh.ondr.koja.KojaEntry
import sh.ondr.koja.jsonSchema
import sh.ondr.koja.toJsonElement

@KojaEntry
fun main() {
    val (server, _) = getServer()
    runMcpServerUsingStdio(server)
}

fun runMcpServer(transport: StdioServerTransport, server: Server) {
    runBlocking {
        server.connect(transport)
        val done = Job()
        server.onClose {
            done.complete()
        }
        done.join()
        println("Server closed")
    }
}

fun runMcpServerUsingStdio(server: Server) {
    // Note: The server will handle listing prompts, tools, and resources automatically.
    // The handleListResourceTemplates will return empty as defined in the Server code.
    val transport = StdioServerTransport(
        inputStream = System.`in`.asSource().buffered(),
        outputStream = System.out.asSink().buffered()
    )

    runMcpServer(transport, server)
}

inline fun <reified T : @JsonSchema Any> getSchema(): JsonObject =
    jsonSchema<T>().toJsonElement().jsonObject["properties"]!!.jsonObject
