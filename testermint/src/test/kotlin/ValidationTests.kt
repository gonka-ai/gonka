import com.productscience.data.InferenceNode
import com.productscience.data.InferenceStatus
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initialize
import com.productscience.invalidNode
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import kotlinx.coroutines.*

class ValidationTests : TestermintTest() {
    @Test
    fun `test valid in parallel`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)

        runBlocking {
            val requests = List(5) { i ->
                launch(Dispatchers.Default) { // specify a dispatcher for parallelism
                    Logger.warn("Starting request $i")
                    highestFunded.makeInferenceRequest(inferenceRequest)
                }
            }
            requests.forEach { it.join() } // ensures all coroutines complete before finishing
        }

        Thread.sleep(10000)
    }

    @Test
    fun `test invalid gets marked invalid`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val oddPair = pairs.last()
        oddPair.api.setNodesTo(invalidNode)
        val invalidResult =
            generateSequence { getInferenceResult(highestFunded) }
                .first { it.executorBefore.id == oddPair.node.addresss }

        highestFunded.node.waitForNextBlock(10)
        val newState = highestFunded.api.getInference(invalidResult.inference.inferenceId)
        assertThat(newState.status).isEqualTo(InferenceStatus.INVALIDATED.value)
    }

    @Test
    fun `test valid with invalid validator gets validated`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val oddPair = pairs.last()
        oddPair.api.setNodesTo(invalidNode)
        val invalidResult =
            generateSequence { getInferenceResult(highestFunded) }
                .first { it.executorBefore.id != oddPair.node.addresss }
        // The oddPair will mark it as invalid and force a vote, which should fail (valid)

        Logger.warn("Got invalid result, waiting for validation.")
        highestFunded.node.waitForNextBlock(10)
        val newState = highestFunded.api.getInference(invalidResult.inference.inferenceId)
        assertThat(newState.status).isEqualTo(InferenceStatus.VALIDATED.value)

    }
}
