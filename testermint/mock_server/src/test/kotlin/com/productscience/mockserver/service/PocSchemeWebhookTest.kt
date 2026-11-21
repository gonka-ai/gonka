package com.productscience.mockserver.service

import com.fasterxml.jackson.module.kotlin.jacksonObjectMapper
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import kotlin.test.*

class PocSchemeWebhookTest {
    @Test
    fun `scheme selects nonce count trajectory and callback version`() {
        val callbacks = LinkedBlockingQueue<Pair<String, String>>()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/") { exchange ->
            callbacks.put(exchange.requestURI.path to exchange.requestBody.bufferedReader().readText())
            exchange.sendResponseHeaders(200, -1)
            exchange.close()
        }
        server.start()
        try {
            val host = HostName("mock")
            val responses = ResponseService()
            responses.setPocResponse(10, host)
            responses.setPocResponse(2, host, ScenarioName("DECODE"))
            val webhooks = WebhookService(responses)
            val json = jacksonObjectMapper()
            for (decode in listOf(false, true)) {
                val version = if (decode) 3 else 2
                val count = if (decode) 2 else 10
                val path = "/v$version/poc-batches/model"
                val request = json.writeValueAsString(mapOf(
                    "url" to "http://127.0.0.1:${server.address.port}$path",
                    "public_key" to "pub", "block_hash" to "hash", "block_height" to 100,
                    "node_id" to 1, "nonces" to (0 until count).toList(),
                    "params" to mapOf("decode" to decode, "max_tokens" to if (decode) 256 else 0),
                ))
                webhooks.processGeneratePocV2Webhook(request, host)
                val generated = assertNotNull(callbacks.poll(10, TimeUnit.SECONDS))
                assertEquals("$path/generated", generated.first)
                val artifacts = json.readTree(generated.second)["artifacts"]
                assertEquals(count, artifacts.size())
                artifacts.forEach { artifact ->
                    if (decode) {
                        assertEquals(257, artifact["k_points_steps"].size())
                        assertTrue(artifact["k_points_steps"].all { it.asInt() in 0..15 })
                        assertFalse(artifact.has("vector_b64"))
                    } else {
                        assertTrue(artifact.has("vector_b64"))
                        assertFalse(artifact.has("k_points_steps"))
                    }
                }
                webhooks.processValidatePocV2Webhook(request)
                val validated = assertNotNull(callbacks.poll(10, TimeUnit.SECONDS))
                assertEquals("$path/validated", validated.first)
                assertEquals(count, json.readTree(validated.second)["n_total"].asInt())
            }
        } finally {
            server.stop(0)
        }
    }
}
