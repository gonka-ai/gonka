package com.productscience.mockserver.service

import io.ktor.client.*
import io.ktor.client.engine.cio.*
import io.ktor.client.request.*
import io.ktor.http.*
import kotlinx.coroutines.*
import com.fasterxml.jackson.module.kotlin.jacksonObjectMapper
import org.slf4j.LoggerFactory

/**
 * Service for handling webhook callbacks.
 */
class WebhookService(private val responseService: ResponseService) {
    private val client = HttpClient(CIO)
    private val mapper = jacksonObjectMapper()
    private val scope = CoroutineScope(Dispatchers.IO)
    private val logger = LoggerFactory.getLogger(WebhookService::class.java)

    // Default delay for validation webhooks (in milliseconds)
    private val validationWebhookDelay = 5000L

    // Default URL for batch validation webhooks
    private val batchValidationWebhookUrl = "http://localhost:9100/v1/poc-batches/validated"

    /**
     * Extracts a value from a JSON string using a JSONPath-like expression.
     * This is a simplified version that only supports direct property access.
     */
    fun extractJsonValue(json: String, path: String): String? {
        if (path.startsWith("$.")) {
            val propertyName = path.substring(2)
            val jsonNode = mapper.readTree(json)
            return jsonNode.get(propertyName)?.asText()
        }
        return null
    }

    /**
     * Sends a webhook POST request after a delay.
     */
    fun sendDelayedWebhook(
        url: String,
        body: String,
        headers: Map<String, String> = mapOf("Content-Type" to "application/json"),
        delayMillis: Long = 1000
    ) {
        scope.launch {
            delay(delayMillis)
            try {
                client.post(url) {
                    headers {
                        headers.forEach { (key, value) ->
                            append(key, value)
                        }
                    }
                    contentType(ContentType.Application.Json)
                    setBody(body)
                }
            } catch (e: Exception) {
                println("Error sending webhook: ${e.message}")
            }
        }
    }

    /**
     * Processes a webhook for the generate POC endpoint.
     */
    fun processGeneratePocWebhook(requestBody: String) {
        try {
            val jsonNode = mapper.readTree(requestBody)
            val url = jsonNode.get("url")?.asText()
            val publicKey = jsonNode.get("public_key")?.asText()
            val blockHash = jsonNode.get("block_hash")?.asText()
            val blockHeight = jsonNode.get("block_height")?.asInt()
            val nodeNumber = jsonNode.get("node_id")?.asInt() ?: 1

            logger.info("Processing generate POC webhook - URL: $url, PublicKey: $publicKey, BlockHeight: $blockHeight, NodeNumber: $nodeNumber")

            if (url != null && publicKey != null && blockHash != null && blockHeight != null) {
                val webhookUrl = "$url/generated"

                // Get the weight from the ResponseService, default to 10 if not set
                val weight = responseService.getPocResponseWeight() ?: 10L
                logger.info("Using weight for POC generation: $weight")

                // Use ResponseService to generate the webhook body
                val webhookBody = responseService.generatePocResponseBody(
                    weight,
                    publicKey,
                    blockHash,
                    blockHeight,
                    nodeNumber,
                )

                logger.info("Sending generate POC webhook to $webhookUrl with weight: $weight")
                sendDelayedWebhook(webhookUrl, webhookBody)
            } else {
                logger.warn("Missing required fields in generate POC webhook request: url=$url, publicKey=$publicKey, blockHash=$blockHash, blockHeight=$blockHeight")
            }
        } catch (e: Exception) {
            logger.error("Error processing generate POC webhook: ${e.message}", e)
        }
    }

    /**
     * Processes a webhook for the validate POC batch endpoint.
     */
    fun processValidatePocBatchWebhook(requestBody: String) {
        try {
            val jsonNode = mapper.readTree(requestBody)
            val publicKey = jsonNode.get("public_key")?.asText()
            val blockHash = jsonNode.get("block_hash")?.asText()
            val blockHeight = jsonNode.get("block_height")?.asInt()
            val nonces = jsonNode.get("nonces")
            val dist = jsonNode.get("dist")

            logger.info("Processing validate POC batch webhook - PublicKey: $publicKey, BlockHeight: $blockHeight")

            if (publicKey != null && blockHash != null && blockHeight != null && nonces != null && dist != null) {
                // Create the webhook body using the values from the request
                val webhookBody = """
                    {
                      "public_key": "$publicKey",
                      "block_hash": "$blockHash",
                      "block_height": $blockHeight,
                      "nonces": $nonces,
                      "dist": $dist,
                      "received_dist": $dist,
                      "r_target": 0.5,
                      "fraud_threshold": 0.1,
                      "n_invalid": 0,
                      "probability_honest": 0.99,
                      "fraud_detected": false
                    }
                """.trimIndent()

                val keyName = (System.getenv("KEY_NAME") ?: "localhost")
                // Use the validation webhook delay
                val webHookUrl = "http://$keyName-api:9100/v1/poc-batches/validated"
                logger.info("Sending batch validation webhook to $webHookUrl with delay: ${validationWebhookDelay}ms")
                logger.debug("Batch validation webhook body: $webhookBody")
                sendDelayedWebhook(webHookUrl, webhookBody, delayMillis = validationWebhookDelay)
            } else {
                logger.warn("Missing required fields in validate POC batch webhook request: publicKey=$publicKey, blockHash=$blockHash, blockHeight=$blockHeight, nonces=$nonces, dist=$dist")
            }
        } catch (e: Exception) {
            logger.error("Error processing validate POC batch webhook: ${e.message}", e)
        }
    }
}
