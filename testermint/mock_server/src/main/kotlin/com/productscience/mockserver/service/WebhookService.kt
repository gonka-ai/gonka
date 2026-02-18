package com.productscience.mockserver.service

import io.ktor.client.*
import io.ktor.client.engine.cio.*
import io.ktor.client.request.*
import io.ktor.http.*
import kotlinx.coroutines.*
import com.fasterxml.jackson.module.kotlin.jacksonObjectMapper
import com.productscience.mockserver.model.latestNonce
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
                logger.error("Error sending webhook: ${e.message}", e)
            }
        }
    }

    /**
     * Processes a webhook for the generate POC endpoint.
     */
    fun processGeneratePocWebhook(requestBody: String, hostName: HostName) {
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
                val weight = responseService.getPocResponseWeight(hostName) ?: 10L

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

    // ========== PoC v2 (artifact-based) webhook handlers ==========

    /**
     * Processes a webhook for PoC v2 init/generate endpoint.
     * Sends artifact batches to the callback URL.
     */
    fun processGeneratePocV2Webhook(requestBody: String, hostName: HostName) {
        try {
            val jsonNode = mapper.readTree(requestBody)
            val url = jsonNode.get("url")?.asText()
            val publicKey = jsonNode.get("public_key")?.asText()
            val blockHash = jsonNode.get("block_hash")?.asText()
            val blockHeight = jsonNode.get("block_height")?.asInt()
            val nodeId = jsonNode.get("node_id")?.asInt() ?: 0

            logger.info("Processing PoC v2 generate webhook - URL: $url, PublicKey: $publicKey, BlockHeight: $blockHeight, NodeId: $nodeId")

            if (url != null && publicKey != null && blockHash != null && blockHeight != null) {
                // Normalize URL: if url already contains /v2/poc-batches, append /generated; otherwise treat as host base
                val webhookUrl = if (url.contains("/v2/poc-batches")) {
                    "$url/generated"
                } else {
                    "$url/v2/poc-batches/generated"
                }

                // Get the weight from the ResponseService, default to 10 if not set
                val weight = responseService.getPocResponseWeight(hostName) ?: 10L

                // Generate unique nonces: atomic counter + nodeId offset
                // Each mock-server container has its own counter, so nodeId ensures uniqueness across nodes
                val start = latestNonce.getAndAdd(weight) + (nodeId * 1_000_000L)
                val artifacts = (0 until weight.toInt()).map { i ->
                    val nonce = start + i
                    // Generate valid FP16 vectors (24 bytes = 12 FP16 values)
                    // FP16 NaN/Inf have exponent bits = 31 (0x7C00-0x7FFF, 0xFC00-0xFFFF)
                    // To avoid these, we mask the high byte to keep exponent < 31
                    val vectorBytes = ByteArray(24) { j ->
                        val rawByte = ((nonce * 2 + j) % 256).toByte()
                        // For odd indices (high byte of FP16), mask to avoid exp=31
                        // exp bits are in bits 2-6 of high byte; masking with 0x7B ensures exp <= 30
                        if (j % 2 == 1) (rawByte.toInt() and 0x7B).toByte() else rawByte
                    }
                    val vectorB64 = java.util.Base64.getEncoder().encodeToString(vectorBytes)
                    """{"nonce": $nonce, "vector_b64": "$vectorB64"}"""
                }.joinToString(", ")

                val webhookBody = """
                    {
                      "public_key": "$publicKey",
                      "node_id": $nodeId,
                      "block_hash": "$blockHash",
                      "block_height": $blockHeight,
                      "artifacts": [$artifacts],
                      "encoding": {"dtype": "f16", "k_dim": 12, "endian": "le"}
                    }
                """.trimIndent()

                logger.info("Sending PoC v2 generate webhook to $webhookUrl with ${weight.toInt()} artifacts")
                sendDelayedWebhook(webhookUrl, webhookBody)
            } else {
                logger.warn("Missing required fields in PoC v2 generate webhook request: url=$url, publicKey=$publicKey, blockHash=$blockHash, blockHeight=$blockHeight")
            }
        } catch (e: Exception) {
            logger.error("Error processing PoC v2 generate webhook: ${e.message}", e)
        }
    }

    /**
     * Processes a webhook for PoC v2 /generate (validation) endpoint.
     * Sends validation results to the callback URL.
     */
    fun processValidatePocV2Webhook(requestBody: String) {
        try {
            val jsonNode = mapper.readTree(requestBody)
            val url = jsonNode.get("url")?.asText()
            val publicKey = jsonNode.get("public_key")?.asText()
            val blockHash = jsonNode.get("block_hash")?.asText()
            val blockHeight = jsonNode.get("block_height")?.asInt()
            val nodeId = jsonNode.get("node_id")?.asInt() ?: 0
            val nonces = jsonNode.get("nonces")

            val nTotal = nonces?.size() ?: 10

            logger.info("Processing PoC v2 validation webhook - PublicKey: $publicKey, BlockHeight: $blockHeight, nTotal: $nTotal")

            // Determine callback URL - normalize to v2 endpoint
            val webhookUrl = if (url != null) {
                // Normalize URL: if url already contains /v2/poc-batches, append /validated; otherwise treat as host base
                if (url.contains("/v2/poc-batches")) {
                    "$url/validated"
                } else {
                    "$url/v2/poc-batches/validated"
                }
            } else {
                val keyName = System.getenv("KEY_NAME") ?: "localhost"
                "http://$keyName-api:9100/v2/poc-batches/validated"
            }

            // Create validation result (happy path: no fraud)
            val webhookBody = """
                {
                  "public_key": "$publicKey",
                  "block_hash": "$blockHash",
                  "block_height": $blockHeight,
                  "node_id": $nodeId,
                  "n_total": $nTotal,
                  "n_mismatch": 0,
                  "mismatch_nonces": [],
                  "p_value": 1.0,
                  "fraud_detected": false
                }
            """.trimIndent()

            logger.info("Sending PoC v2 validation webhook to $webhookUrl with delay: ${validationWebhookDelay}ms")
            sendDelayedWebhook(webhookUrl, webhookBody, delayMillis = validationWebhookDelay)
        } catch (e: Exception) {
            logger.error("Error processing PoC v2 validation webhook: ${e.message}", e)
        }
    }
}
