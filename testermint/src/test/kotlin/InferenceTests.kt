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

class InferenceTests : TestermintTest() {
    @Test
    fun `valid inference`() {
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        val genesisAddress = genesis.node.getAddress()
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
        assertThat(response.code).isZero()
        println(response)
        val inference = genesis.node.getInference(signature)
        assertThat(inference.inference.inferenceId).isEqualTo(signature)
        assertThat(inference.inference.requestTimestamp).isEqualTo(timestamp)
        assertThat(inference.inference.transferredBy).isEqualTo(genesisAddress)
        assertThat(inference.inference.transferSignature).isEqualTo(taSignature)
    }

    @Test
    fun `submit duplicate transaction`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        assertThat(response.code).isZero()
        println(response)
        val response2 = genesis.submitMessage(message)
        println(response2)
        assertThat(response2.code).isNotZero()
    }

    @Test
    fun `submit StartInference with bad dev signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgStartInference(
            creator = genesisAddress,
            inferenceId = signature + "bad",
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
        println(response)
        assertThat(response.code).isNotZero()
    }

    @Test
    fun `submit StartInference with bad TA signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
            transferSignature = taSignature + "bad"
        )
        val response = genesis.submitMessage(message)
        println(response)
        assertThat(response.code).isNotZero()
    }

    @Test
    fun `old timestamp`() {
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().minusSeconds(20).toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)

        assertThatThrownBy {
            genesis.api.makeInferenceRequest(inferenceRequest, genesisAddress, signature, timestamp)
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 400 Bad Request")
    }

    @Test
    fun `repeated request rejected`() {
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        val inference = genesis.node.getInference(valid.id).inference
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
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature + "wrong",
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
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        assertThatThrownBy {
            genesis.api.makeExecutorInferenceRequest(
                inferenceRequest,
                genesisAddress,
                signature,
                genesisAddress,
                taSignature + "wrong",
                timestamp
            )
        }.isInstanceOf(FuelError::class.java)
            .hasMessageContaining("HTTP Exception 401 Unauthorized")
    }

    @Test
    fun `executor rejects old timestamp`() {
        val timestamp = Instant.now().minusSeconds(20).toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        genesis.waitForNextInferenceWindow()
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            originalPrompt = inferenceRequest,
        )
        val response = genesis.submitMessage(message)
        println(response)
        assertThat(response.code).isZero()
    }

    @Test
    fun `finish inference validates dev signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature + "wrong",
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
        )
        val response = genesis.submitMessage(message)
        println(response)
        assertThat(response.code).isNotZero()
    }

    @Test
    fun `finish inference validates ta signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
        val signature = genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress, null)
        val taSignature =
            genesis.node.signPayload(inferenceRequest + timestamp.toString() + genesisAddress + genesisAddress, null)
        val message = MsgFinishInference(
            creator = genesisAddress,
            inferenceId = signature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature + "wrong",
            responseHash = "fjdsf",
            responsePayload = "AI is cool",
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = taSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
        )
        val response = genesis.submitMessage(message)
        println(response)
        assertThat(response.code).isNotZero()
    }

    @Test
    fun `finish inference validates ea signature`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getAddress()
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
            executorSignature = taSignature + "wrong",
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
        )
        val response = genesis.submitMessage(message)
        println(response)
        assertThat(response.code).isNotZero()
    }


    companion object {
        @JvmStatic
        @BeforeAll
        fun getCluster(): Unit {
            val (clus, gen) = initCluster()
            cluster = clus
            genesis = gen
        }

        lateinit var cluster: LocalCluster
        lateinit var genesis: LocalInferencePair
    }

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