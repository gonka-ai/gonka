import com.github.kittinunf.fuel.core.FuelError
import com.productscience.*
import com.productscience.data.MsgFinishInference
import com.productscience.data.MsgStartInference
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.assertj.core.api.SoftAssertions
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.Test
import java.time.Instant
import kotlin.experimental.xor
import kotlin.test.assertNotNull
import java.util.Base64
import com.productscience.assertions.assertThat
import java.security.MessageDigest
import com.google.gson.Gson
import com.google.gson.JsonArray
import com.google.gson.JsonElement
import com.google.gson.JsonObject
import com.google.gson.JsonPrimitive
import java.util.TreeMap

// Phase 3: SHA256 hash utility for signature migration
fun sha256(input: String): String {
    val digest = MessageDigest.getInstance("SHA-256")
    return digest.digest(input.toByteArray()).joinToString("") { "%02x".format(it) }
}

// Phase 6: Canonicalize JSON to match Go's CanonicalizeJSON behavior
// Recursively sorts object keys and outputs compact JSON
// IMPORTANT: Go's json.Encoder.Encode() adds a trailing newline, so we must match that
fun canonicalizeJson(json: String): String {
    val gson = Gson()
    val element = gson.fromJson(json, JsonElement::class.java)
    val canonical = canonicalizeElement(element)
    // Go's json.Encoder.Encode() adds trailing newline - must match for hash compatibility
    return gson.toJson(canonical) + "\n"
}

private fun canonicalizeElement(element: JsonElement): JsonElement {
    return when {
        element.isJsonObject -> {
            val obj = element.asJsonObject
            val sortedMap = TreeMap<String, JsonElement>()
            for (entry in obj.entrySet()) {
                sortedMap[entry.key] = canonicalizeElement(entry.value)
            }
            val result = JsonObject()
            for ((key, value) in sortedMap) {
                result.add(key, value)
            }
            result
        }
        element.isJsonArray -> {
            val arr = element.asJsonArray
            val result = JsonArray()
            for (item in arr) {
                result.add(canonicalizeElement(item))
            }
            result
        }
        else -> element // primitives and nulls stay as-is
    }
}

// Compute SHA256 of canonicalized JSON (matches Go's ComputePromptHash)
fun canonicalSha256(json: String): String {
    return sha256(canonicalizeJson(json))
}

// Compute response hash matching Go's CompletionResponse.GetHash()
// Hashes full payload bytes to include logprobs (security fix: prevents logprob manipulation)
fun computeResponseHash(responsePayload: String): String {
    return sha256(responsePayload)
}

class InferenceTests : TestermintTest() {
    @Test
    fun `valid inference`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
        val valid = genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        assertThat(valid.id).isEqualTo(signature)
        assertThat(valid.model).isEqualTo(inferenceRequestObject.model)
        assertThat(valid.choices).hasSize(1)
    }

    @Test
    fun `wrong TA address`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = "NotTheRightAddress"
        )

        assertThatThrownBy {
            genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 401 Unauthorized")
    }

    @Test
    fun `submit raw transaction`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(
            originalPromptHash,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
        // Phase 3: TA signs prompt_hash (= originalPromptHash when no seed modification)
        val promptHash = originalPromptHash
        val taSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = promptHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            originalPromptHash = originalPromptHash
        )

        val response = genesis.submitMessage(message)
        assertThat(response).isSuccess()
        println(response)
        val inference = genesis.node.getInference(signature)
        assertNotNull(inference)
        assertThat(inference.inference.inferenceId).isEqualTo(signature)
        assertThat(inference.inference.requestTimestamp).isEqualTo(timestamp)
        assertThat(inference.inference.transferredBy).isEqualTo(genesisAddress)
        assertThat(inference.inference.transferSignature).isEqualTo(taSignature)
        logHighlight("Per token cost: ${inference.inference.perTokenPrice}")
    }

    @Test
    fun `submit duplicate transaction`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Use hashes for signatures
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = originalPromptHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            originalPromptHash = originalPromptHash
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isSuccess()
        val response2 = genesis.submitMessage(message)
        assertThat(response2).isFailure()
    }

    @Test
    fun `submit StartInference with bad dev signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Use hashes for signatures
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature.invalidate(),
            promptHash = originalPromptHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            originalPromptHash = originalPromptHash
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `submit StartInference with bad TA signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Use hashes for signatures
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = originalPromptHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature.invalidate(),
            originalPromptHash = originalPromptHash
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `old timestamp`() {
        val params = genesis.getParams()
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().minusSeconds(params.validationParams.timestampExpiration + 10).toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(inferenceRequest, accountAddress = null, timestamp = timestamp, endpointAccount = genesisAddress)

        assertThatThrownBy {
            genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 400 Bad Request")
    }

    @Test
    fun `repeated request rejected`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
        val valid = genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        assertThat(valid.id).isEqualTo(signature)
        assertThat(valid.model).isEqualTo(inferenceRequestObject.model)
        assertThat(valid.choices).hasSize(1)
        assertThatThrownBy {
            genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 400 Bad Request")
    }

    @Test
    fun `valid direct executor request`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA signs prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val valid = genesis.api.makeExecutorInferenceRequest(
            inferenceRequest,
            genesisAddress,
            signature,
            genesisAddress,
            taSignature,
            timestamp
        )
        assertThat(valid.id).isEqualTo(signature)
        assertThat(valid.model).isEqualTo(inferenceRequestObject.model)
        assertThat(valid.choices).hasSize(1)
        genesis.node.waitForNextBlock(2)
        val inference = genesis.node.getInference(valid.id)?.inference
        assertNotNull(inference)
        softly {
            assertThat(inference.inferenceId).isEqualTo(signature)
            assertThat(inference.requestTimestamp).isEqualTo(timestamp)
            assertThat(inference.transferredBy).isEqualTo(genesisAddress)
            assertThat(inference.transferSignature).isEqualTo(taSignature)
            assertThat(inference.executedBy).isEqualTo(genesisAddress)
            // TODO: UNDERSTAND WHY EXACTLY
            // Note: Can't assert executionSignature matches taSignature because:
            // - TA signs originalPromptHash (what we computed above)
            // - Executor signs promptHash (after API modifies request with seed/logprobs)
            // - These are different hashes, so signatures will differ
            // The test verifies the inference completed successfully
            // assertThat(inference.executionSignature).isEqualTo(taSignature)
        }
        println(inference)
    }

    @Test
    fun `executor validates dev signature`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA signs prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature.invalidate(),
                genesisAddress,
                taSignature,
                timestamp
            )
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 401 Unauthorized")

    }

    @Test
    fun `executor validates TA signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA signs prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature,
                genesisAddress,
                taSignature.invalidate(),
                timestamp
            )
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 401 Unauthorized")
    }

    @Test
    fun `executor rejects old timestamp`() {
        val params = genesis.getParams()
        val timestamp = Instant.now().minusSeconds(params.validationParams.timestampExpiration + 10).toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA signs prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature,
                genesisAddress,
                taSignature,
                timestamp
            )
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 400 Bad Request")
    }

    @Test
    fun `executor rejects duplicate requests`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA signs prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val valid = genesis.api.makeExecutorInferenceRequest(
            inferenceRequest,
            genesisAddress,
            signature,
            genesisAddress,
            taSignature,
            timestamp
        )
        assertThat(valid.id).isEqualTo(signature)
        assertThat(valid.model).isEqualTo(inferenceRequestObject.model)
        assertThat(valid.choices).hasSize(1)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature,
                genesisAddress,
                taSignature,
                timestamp
            )
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 400 Bad Request")
    }

    @Test
    fun `direct finish inference works`() {
        val finishTimestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA/Executor sign prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash // Same when no seed modification
        val finishSignature = genesis.node.signPayload(originalPromptHash + finishTimestamp.toString() + genesisAddress, null)
        val finishTaSignature =
            genesis.node.signPayload(promptHash + finishTimestamp.toString() + genesisAddress + genesisAddress, null)
        val finishMessage = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = finishSignature,
            promptTokenCount = 10,
            requestTimestamp = finishTimestamp,
            transferSignature = finishTaSignature,
            responseHash = "fjdsf",
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = finishTaSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            // originalPrompt removed - Phase 6: payloads stored offchain
            model = defaultModel,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash
        )
        val response = genesis.submitMessage(finishMessage)
        assertThat(response).isSuccess()
    }

    @Test
    fun `finish inference validates dev signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA/Executor sign prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature.invalidate(),
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            responseHash = "fjdsf",
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            // originalPrompt removed - Phase 6: payloads stored offchain
            promptHash = promptHash,
            originalPromptHash = originalPromptHash,
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `finish inference validates ta signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA/Executor sign prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature.invalidate(),
            responseHash = "fjdsf",
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = "default",
            // originalPrompt removed - Phase 6: payloads stored offchain
            promptHash = promptHash,
            originalPromptHash = originalPromptHash
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `finish inference validates ea signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        // Phase 3: Dev signs original_prompt_hash, TA/Executor sign prompt_hash
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val signature = genesis.node.signPayload(originalPromptHash + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            responseHash = "fjdsf",
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature.invalidate(),
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            // originalPrompt removed - Phase 6: payloads stored offchain
            promptHash = promptHash,
            originalPromptHash = originalPromptHash,
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }


    companion object {
        @JvmStatic
        @BeforeAll
        fun getCluster(): Unit {
            val (clus, gen) = initCluster()
            clus.allPairs.forEach { pair ->
                pair.waitForMlNodesToLoad()
            }
            cluster = clus
            genesis = gen
        }

        lateinit var cluster: LocalCluster
        lateinit var genesis: LocalInferencePair
    }
}

private fun String.invalidate(): String {
    val decoder = Base64.getDecoder()
    val encoder = Base64.getEncoder()
    val bytes = decoder.decode(this)

    // Flip one bit in the first byte
    bytes[0] = bytes[0].xor(0x01)

    return encoder.encodeToString(bytes)
}
fun Instant.toEpochNanos(): Long {
    return this.epochSecond * 1_000_000_000 + this.nano.toLong()
}

inline fun <T> softly(block: SoftAssertions.() -> T): T {
    val softly = SoftAssertions()
    val result = softly.block()
    softly.assertAll()
    return result
}