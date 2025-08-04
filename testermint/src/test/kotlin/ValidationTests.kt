import com.productscience.*
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.data.ModelConfig
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.time.Instant
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class ValidationTests : TestermintTest() {
    @Test
    fun `test valid in parallel`() {
        val (_, genesis) = initCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                )
            ),
            reboot = true
        )

        genesis.node.waitForMinimumBlock(35)
        logSection("Making inference requests in parallel")
        val requests = 50
        val statuses = runParallelInferences(genesis, requests, maxConcurrentRequests = requests)
        Logger.info("Statuses: $statuses")

        logSection("Verifying inference statuses")
        assertThat(statuses.map { status ->
            InferenceStatus.entries.first { it.value == status }
        }).allMatch {
            it == InferenceStatus.VALIDATED || it == InferenceStatus.FINISHED
        }
        assertThat(statuses).hasSize(requests)

        Thread.sleep(10000)
    }

    @Test
    @Tag("unstable")
    fun `test invalid gets marked invalid`() {
        var tries = 3
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        val badResponse = defaultInferenceResponseObject.withMissingLogit()
        oddPair.mock?.setInferenceResponse(badResponse)
        var newState: InferencePayload
        do {
            logSection("Trying to get invalid inference. Tries left: $tries")
            newState = getInferenceValidationState(genesis, oddPair)
        } while (newState.statusEnum != InferenceStatus.INVALIDATED && tries-- > 0)
        logSection("Verifying invalidation")
        assertThat(newState.statusEnum).isEqualTo(InferenceStatus.INVALIDATED)
    }

    @Test
    @Timeout(15, unit = TimeUnit.MINUTES)
    @Tag("unstable")
    fun `test invalid gets removed`() {
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        oddPair.mock?.setInferenceResponse(defaultInferenceResponseObject.withMissingLogit())
        logSection("Getting many invalid inferences for ${oddPair.name}")
        val invalidResult =
            generateSequence { getInferenceResult(genesis) }
                .filter {
                    Logger.warn("Got result: ${it.executorBefore.id} ${it.executorAfter.id}")
                    it.executorBefore.id == oddPair.node.getAddress()
                }
                .take(5)
                .toList()
        Logger.warn("Got invalid result, waiting for invalidation.")

        genesis.markNeedsReboot()
        logSection("Waiting for removal")
        genesis.node.waitForNextBlock(10)
        val participants = genesis.api.getParticipants()
        participants.forEach { Logger.warn("Participant: $it") }
    }

    @Test
    @Tag("unstable")
    fun `test valid with invalid validator gets validated`() {
        val (cluster, genesis) = initCluster()
        val oddPair = cluster.joinPairs.last()
        oddPair.mock?.setInferenceResponse(defaultInferenceResponseObject.withMissingLogit())
        logSection("Getting invalid invalidation")
        val invalidResult =
            generateSequence { getInferenceResult(genesis) }
                .first { it.executorBefore.id != oddPair.node.getAddress() }
        // The oddPair will mark it as invalid and force a vote, which should fail (valid)

        Logger.warn("Got invalid result, waiting for validation.")
        logSection("Waiting for revalidation")
        genesis.node.waitForNextBlock(10)
        logSection("Verifying revalidation")
        val newState = genesis.api.getInference(invalidResult.inference.inferenceId)

        assertThat(newState.statusEnum).isEqualTo(InferenceStatus.VALIDATED)

    }
}

val InferencePayload.statusEnum: InferenceStatus
    get() = InferenceStatus.entries.first { it.value == status }

fun getInferenceValidationState(
    highestFunded: LocalInferencePair,
    oddPair: LocalInferencePair,
    modelName: String? = null
): InferencePayload {
    val invalidResult =
        generateSequence { getInferenceResult(highestFunded, modelName) }
            .take(10)
            .firstOrNull {
                Logger.warn("Got result: ${it.executorBefore.id} ${it.executorAfter.id}")
                it.executorBefore.id == oddPair.node.getAddress()
            }
    if (invalidResult == null) {
        error("Did not get result from invalid pair(${oddPair.node.getAddress()}) in time")
    }

    Logger.warn(
        "Got invalid result, waiting for invalidation. " +
                "Output was:${invalidResult.inference.responsePayload}"
    )

    highestFunded.node.waitForNextBlock(3)
    val newState = highestFunded.api.getInference(invalidResult.inference.inferenceId)
    return newState
}
