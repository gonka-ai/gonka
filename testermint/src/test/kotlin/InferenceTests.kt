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

class InferenceTests : TestermintTest() {
    @Test
    fun `valid inference`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(
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
        val signature = genesis.node.signPayload(
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
        val signature = genesis.node.signPayload(
            inferenceRequest,
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = "not_verified",
            promptPayload = inferenceRequest,
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = "not_verified",
            promptPayload = inferenceRequest,
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature.invalidate(),
            promptHash = "not_verified",
            promptPayload = "Say Hello",
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `submit StartInference with bad TA signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptHash = "not_verified",
            promptPayload = "Say Hello",
            model = "gpt-o3",
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature.invalidate()
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)

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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
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
        genesis.node.waitForNextBlock()
        val inference = genesis.node.getInference(valid.id)?.inference
        assertNotNull(inference)
        softly {
            assertThat(inference.inferenceId).isEqualTo(signature)
            assertThat(inference.requestTimestamp).isEqualTo(timestamp)
            assertThat(inference.transferredBy).isEqualTo(genesisAddress)
            assertThat(inference.transferSignature).isEqualTo(taSignature)
            assertThat(inference.executedBy).isEqualTo(genesisAddress)
            assertThat(inference.executionSignature).isEqualTo(taSignature)
        }
        println(inference)
    }

    @Test
    fun `executor validates dev signature`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
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
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
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
        val finishSignature = genesis.node.signPayload(inferenceRequest + finishTimestamp.toString() + genesisAddress, null)
        val finishTaSignature =
            genesis.node.signPayload(inferenceRequest + finishTimestamp.toString() + genesisAddress + genesisAddress, null)
        val finishMessage = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = finishSignature,
            promptTokenCount = 10,
            requestTimestamp = finishTimestamp,
            transferSignature = finishTaSignature,
            responseHash = "fjdsf",
            responsePayload = "AI is cool",
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = finishTaSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            originalPrompt = inferenceRequest,
            model = defaultModel
        )
        val response = genesis.submitMessage(finishMessage)
        assertThat(response).isSuccess()
    }

    @Test
    fun `finish inference validates dev signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature.invalidate(),
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            responseHash = "fjdsf",
            responsePayload = "AI is cool",
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            originalPrompt = inferenceRequest,
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `finish inference validates ta signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature.invalidate(),
            responseHash = "fjdsf",
            responsePayload = "AI is cool",
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = "default",
            originalPrompt = inferenceRequest
        )
        val response = genesis.submitMessage(message)
        assertThat(response).isFailure()
    }

    @Test
    fun `finish inference validates ea signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            responseHash = "fjdsf",
            responsePayload = "AI is cool",
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature.invalidate(),
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            originalPrompt = inferenceRequest,
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