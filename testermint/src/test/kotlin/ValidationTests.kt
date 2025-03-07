import com.productscience.LocalInferencePair
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.defaultInferenceResponseObject
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initCluster
import com.productscience.initialize
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class ValidationTests : TestermintTest() {
    @Test
    fun `test valid in parallel`() {
        val (_, genesis) = initCluster()
        genesis.waitForFirstPoC()

        val statuses = runParallelInferences(genesis, 100)
        Logger.info("Statuses: $statuses")

        // Some will be validated, some will not.
        assertThat(statuses).allMatch {
            it == InferenceStatus.VALIDATED.value || it == InferenceStatus.FINISHED.value
        }

        Thread.sleep(10000)
    }

    @Test
    fun `test invalid gets marked invalid`() {
        var tries = 3
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        val badResponse = defaultInferenceResponseObject.withMissingLogit()
        oddPair.mock?.setInferenceResponse(badResponse)
        var newState: InferencePayload
        do {
            newState = getInferenceValidationState(genesis, oddPair)
        } while (newState.status != InferenceStatus.INVALIDATED.value && tries-- > 0)
        assertThat(newState.status).isEqualTo(InferenceStatus.INVALIDATED.value)
    }

    private fun getInferenceValidationState(
        highestFunded: LocalInferencePair,
        oddPair: LocalInferencePair,
    ): InferencePayload {
        val invalidResult =
            generateSequence { getInferenceResult(highestFunded) }
                .first {
                    Logger.warn("Got result: ${it.executorBefore.id} ${it.executorAfter.id}")
                    it.executorBefore.id == oddPair.node.addresss
                }

        Logger.warn(
            "Got invalid result, waiting for invalidation. " +
                    "Output was:${invalidResult.inference.responsePayload}"
        )

        highestFunded.node.waitForNextBlock(10)
        val newState = highestFunded.api.getInference(invalidResult.inference.inferenceId)
        return newState
    }

    @Test
    fun `test invalid gets removed`() {
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        oddPair.mock?.setInferenceResponse(defaultInferenceResponseObject.withMissingLogit())
        val invalidResult =
            generateSequence { getInferenceResult(genesis) }
                .filter {
                    Logger.warn("Got result: ${it.executorBefore.id} ${it.executorAfter.id}")
                    it.executorBefore.id == oddPair.node.addresss
                }
                .take(5)
                .toList()
        Logger.warn("Got invalid result, waiting for invalidation.")

        genesis.node.waitForNextBlock(10)
        val participants = genesis.api.getParticipants()
        participants.forEach { Logger.warn("Participant: ${it.id} ${it.balance}") }

    }

    @Test
    fun `test valid with invalid validator gets validated`() {
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        oddPair.mock?.setInferenceResponse(defaultInferenceResponseObject.withMissingLogit())
        val invalidResult =
            generateSequence { getInferenceResult(genesis) }
                .first { it.executorBefore.id != oddPair.node.addresss }
        // The oddPair will mark it as invalid and force a vote, which should fail (valid)

        Logger.warn("Got invalid result, waiting for validation.")
        genesis.node.waitForNextBlock(10)
        val newState = genesis.api.getInference(invalidResult.inference.inferenceId)
        assertThat(newState.status).isEqualTo(InferenceStatus.VALIDATED.value)

    }

    @Test
    fun `test reputation increases from epoch to epoch`() {
        val (cluster, genesis) = initCluster()
        runParallelInferences(genesis, 100)


    }
}

fun runParallelInferences(
    genesis: LocalInferencePair,
    count: Int,
    waitForBlocks: Int = 20,
): List<Int> = runBlocking {
    // Launch coroutines with async and collect the deferred results
    val requests = List(count) { i ->
        async(Dispatchers.Default) { // specify a dispatcher for parallelism
            Logger.warn("Starting request $i")
            genesis.makeInferenceRequest(inferenceRequest)
        }
    }

    // Wait for all requests to complete and collect their results
    val results = requests.map { it.await() }

    genesis.node.waitForNextBlock(waitForBlocks)

    // Return statuses
    results.map { result ->
        val inference = genesis.api.getInference(result.id)
        inference.status
    }
}

