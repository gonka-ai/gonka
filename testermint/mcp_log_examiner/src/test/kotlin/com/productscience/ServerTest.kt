package com.productscience

import io.modelcontextprotocol.kotlin.sdk.server.StdioServerTransport
import kotlinx.coroutines.GlobalScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.io.asSink
import kotlinx.io.asSource
import kotlinx.io.buffered
import kotlinx.serialization.json.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import java.io.File
import java.io.PipedInputStream
import java.io.PipedOutputStream
import java.util.concurrent.TimeUnit

class ServerTest {
    // Pipes for communication with the server
    private lateinit var testToServer: PipedOutputStream
    private lateinit var serverToTest: PipedInputStream
    private lateinit var serverToTestOutput: PipedOutputStream
    private lateinit var testFromServer: PipedInputStream

    // Server job
    private lateinit var serverJob: Job

    /**
     * Build a JSON-RPC request for calling a tool
     *
     * @param toolName The name of the tool to call
     * @param arguments The arguments to pass to the tool
     * @param id The request ID
     * @return A JsonObject representing the request
     */
    private fun buildRequest(toolName: String, arguments: JsonObject, id: Int): JsonObject {
        return buildJsonObject {
            put("method", "tools/call")
            put("params", buildJsonObject {
                put("name", toolName)
                put("arguments", arguments)
            })
            put("jsonrpc", "2.0")
            put("id", id)
        }
    }

    @BeforeEach
    fun setup() {
        println("[DEBUG_LOG] Setting up test")

        // Create pipes for bidirectional communication
        testToServer = PipedOutputStream()
        serverToTest = PipedInputStream(testToServer)

        serverToTestOutput = PipedOutputStream()
        testFromServer = PipedInputStream(serverToTestOutput)

        println("[DEBUG_LOG] Pipes created")

        // Set up the server with tools
        val (server, _) = getServer()

        println("[DEBUG_LOG] Server tools set up")

        // Create a transport using our pipes
        val transport = StdioServerTransport(
            inputStream = serverToTest.asSource().buffered(),
            outputStream = serverToTestOutput.asSink().buffered()
        )

        println("[DEBUG_LOG] Transport created")

        // Start the server in a coroutine
        serverJob = GlobalScope.launch {
            println("[DEBUG_LOG] Server coroutine started")
            runBlocking {
                println("[DEBUG_LOG] Connecting server to transport")
                server.connect(transport)
                val done = Job()
                server.onClose {
                    println("[DEBUG_LOG] Server closed callback triggered")
                    done.complete()
                }
                println("[DEBUG_LOG] Waiting for server to close")
                done.join()
                println("[DEBUG_LOG] Server closed for test")
            }
        }

        // Give the server time to start
        println("[DEBUG_LOG] Waiting for server to start")
        TimeUnit.MILLISECONDS.sleep(2000)
        println("[DEBUG_LOG] Server should be ready now")
    }

    @AfterEach
    fun tearDown() {
        println("[DEBUG_LOG] Tearing down test")

        // Close the pipes
        println("[DEBUG_LOG] Closing pipes")
        testToServer.close()
        serverToTest.close()
        serverToTestOutput.close()
        testFromServer.close()

        // Cancel the server job
        println("[DEBUG_LOG] Cancelling server job")
        serverJob.cancel()

        // Reset the logAnalyzer
        println("[DEBUG_LOG] Resetting logAnalyzer")
        println("[DEBUG_LOG] Test teardown complete")
    }

    private fun getTestLogFile(): File {
        val classLoader = javaClass.classLoader
        return File(
            classLoader.getResource("testermint.log")?.file ?: throw IllegalStateException("Test log file not found")
        )
    }

    @Test
    fun `should load log file via server request`() {
        // Get the test log file
        val logFile = getTestLogFile()

        println("[DEBUG_LOG] Test log file path: ${logFile.absolutePath}")
        println("[DEBUG_LOG] Test log file exists: ${logFile.exists()}")

        // Create a request to load the log file
        val arguments = buildJsonObject {
            put("logfile", logFile.absolutePath)
        }
        val request = buildRequest("load-log", arguments, 1)

        println("[DEBUG_LOG] Sending request: ${request}")

        // Send the request to the server
        testToServer.write(request.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server
        val responseBytes = ByteArray(4096)
        val bytesRead = testFromServer.read(responseBytes)
        val responseString = String(responseBytes, 0, bytesRead)

        println("[DEBUG_LOG] Response received: ${responseString}")

        // Parse the response
        val responseJson = Json.parseToJsonElement(responseString).jsonObject

        println("[DEBUG_LOG] Response received and parsed successfully")

        // Verify the response
        assertThat(responseJson["id"]?.jsonPrimitive?.int).isEqualTo(1)
        assertThat(responseJson["result"]).isNotNull()
    }

    @Test
    fun `should retrieve database schema via server request`() {
        // First load a log file
        val logFile = getTestLogFile()

        // Load the log file
        val loadArguments = buildJsonObject {
            put("logfile", logFile.absolutePath)
        }
        val loadRequest = buildRequest("load-log", loadArguments, 1)

        // Send the load request to the server
        testToServer.write(loadRequest.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server (for the load request)
        val loadResponseBytes = ByteArray(4096)
        val loadBytesRead = testFromServer.read(loadResponseBytes)

        // Create a request to get the log schema
        val arguments = buildJsonObject {
            put("dummy", "")
        }
        val request = buildRequest("log-schema", arguments, 2)

        println("[DEBUG_LOG] Sending schema request: ${request}")

        // Send the request to the server
        testToServer.write(request.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server
        val responseBytes = ByteArray(4096)
        val bytesRead = testFromServer.read(responseBytes)
        val responseString = String(responseBytes, 0, bytesRead)

        println("[DEBUG_LOG] Schema response received: ${responseString}")

        // Parse the response
        val responseJson = Json.parseToJsonElement(responseString).jsonObject

        println("[DEBUG_LOG] Schema response received and parsed successfully")

        // Verify the response
        assertThat(responseJson["id"]?.jsonPrimitive?.int).isEqualTo(2)
        assertThat(responseJson["result"]).isNotNull()

        // Verify that the response contains schema information
        val content =
            responseJson["result"]?.jsonObject?.get("content")?.jsonArray?.get(0)?.jsonObject?.get("text")?.jsonPrimitive?.content
        println("[DEBUG_LOG] Schema content: ${content}")
        assertThat(content).isNotNull()
        assertThat(content).contains("LogEntries")
    }

    @Test
    fun `should execute SQL query via server request`() {
        // First load a log file
        val logFile = getTestLogFile()

        // Load the log file
        val loadArguments = buildJsonObject {
            put("logfile", logFile.absolutePath)
        }
        val loadRequest = buildRequest("load-log", loadArguments, 1)

        // Send the load request to the server
        testToServer.write(loadRequest.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server (for the load request)
        val loadResponseBytes = ByteArray(4096)
        val loadBytesRead = testFromServer.read(loadResponseBytes)

        // Create a request to query the log
        val arguments = buildJsonObject {
            put("query", "SELECT COUNT(*) as count FROM LogEntries")
            put("format", "text")
        }
        val request = buildRequest("log-query", arguments, 3)

        println("[DEBUG_LOG] Sending query request: ${request}")

        // Send the request to the server
        testToServer.write(request.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server
        val responseBytes = ByteArray(4096)
        val bytesRead = testFromServer.read(responseBytes)
        val responseString = String(responseBytes, 0, bytesRead)

        println("[DEBUG_LOG] Query response received: ${responseString}")

        // Parse the response
        val responseJson = Json.parseToJsonElement(responseString).jsonObject

        println("[DEBUG_LOG] Query response received and parsed successfully")

        // Verify the response
        assertThat(responseJson["id"]?.jsonPrimitive?.int).isEqualTo(3)
        assertThat(responseJson["result"]).isNotNull()

        // Verify that the response contains query results
        val content =
            responseJson["result"]?.jsonObject?.get("content")?.jsonArray?.get(0)?.jsonObject?.get("text")?.jsonPrimitive?.content
        println("[DEBUG_LOG] Query content: ${content}")
        assertThat(content).isNotNull()
        assertThat(content).contains("count")
    }

    @Test
    fun `should respect default limit when executing SQL query`() {
        // First load a log file
        val logFile = getTestLogFile()

        // Load the log file
        val loadArguments = buildJsonObject {
            put("logfile", logFile.absolutePath)
        }
        val loadRequest = buildRequest("load-log", loadArguments, 1)

        // Send the load request to the server
        testToServer.write(loadRequest.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server (for the load request)
        val loadResponseBytes = ByteArray(4096)
        val loadBytesRead = testFromServer.read(loadResponseBytes)

        // Create a request to query the log with a query that returns all records
        val arguments = buildJsonObject {
            put("query", "SELECT * FROM LogEntries")
            put("format", "text")
            // Not specifying limit, should use default of 100
        }
        val request = buildRequest("log-query", arguments, 4)

        println("[DEBUG_LOG] Sending query request with default limit: ${request}")

        // Send the request to the server
        testToServer.write(request.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server
        val responseBytes = ByteArray(4096)
        val bytesRead = testFromServer.read(responseBytes)
        val responseString = String(responseBytes, 0, bytesRead)

        println("[DEBUG_LOG] Query response received: ${responseString}")

        // Parse the response
        val responseJson = Json.parseToJsonElement(responseString).jsonObject

        println("[DEBUG_LOG] Query response received and parsed successfully")

        // Verify the response
        assertThat(responseJson["id"]?.jsonPrimitive?.int).isEqualTo(4)
        assertThat(responseJson["result"]).isNotNull()

        // Get the content of the response
        val content =
            responseJson["result"]?.jsonObject?.get("content")?.jsonArray?.get(0)?.jsonObject?.get("text")?.jsonPrimitive?.content
        println("[DEBUG_LOG] Query content: ${content}")
        assertThat(content).isNotNull()

        // If the number of records exceeds the default limit (50), the response should contain a message about the limit
        // Otherwise, it should contain the actual records
        assertThat(content).contains("Total matching records")
        assertThat(content).contains("exceeds the limit of 50 records")
    }

    @Test
    fun `should respect custom limit when executing SQL query`() {
        // First load a log file
        val logFile = getTestLogFile()

        // Load the log file
        val loadArguments = buildJsonObject {
            put("logfile", logFile.absolutePath)
        }
        val loadRequest = buildRequest("load-log", loadArguments, 1)

        // Send the load request to the server
        testToServer.write(loadRequest.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        val loadResponseBytes = ByteArray(4096)
        val loadBytesRead = testFromServer.read(loadResponseBytes)

        val customLimit = 5

        // Create a request to query the log with a custom limit
        val arguments = buildJsonObject {
            put("query", "SELECT * FROM LogEntries")
            put("format", "text")
            put("limit", customLimit)
        }
        val request = buildRequest("log-query", arguments, 5)

        println("[DEBUG_LOG] Sending query request with custom limit $customLimit: ${request}")

        // Send the request to the server
        testToServer.write(request.toString().toByteArray())
        testToServer.write("\n".toByteArray())
        testToServer.flush()

        // Give the server time to process the request
        TimeUnit.MILLISECONDS.sleep(1000)

        // Read the response from the server
        val responseBytes = ByteArray(4096)
        val bytesRead = testFromServer.read(responseBytes)
        val responseString = String(responseBytes, 0, bytesRead)

        println("[DEBUG_LOG] Query response received: ${responseString}")

        // Parse the response
        val responseJson = Json.parseToJsonElement(responseString).jsonObject

        println("[DEBUG_LOG] Query response received and parsed successfully")

        // Verify the response
        assertThat(responseJson["id"]?.jsonPrimitive?.int).isEqualTo(5)
        assertThat(responseJson["result"]).isNotNull()

        // Get the content of the response
        val content =
            responseJson["result"]?.jsonObject?.get("content")?.jsonArray?.get(0)?.jsonObject?.get("text")?.jsonPrimitive?.content
        println("[DEBUG_LOG] Query content: ${content}")
        assertThat(content).isNotNull()

        // If the number of records exceeds the custom limit, the response should contain a message about the limit
        assertThat(content).contains("Total matching records")
        assertThat(content).contains("exceeds the limit of $customLimit records")
    }
}
