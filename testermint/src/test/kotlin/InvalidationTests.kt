import com.productscience.*
import com.productscience.data.AppState
import com.productscience.data.BandwidthLimitsParams
import com.productscience.data.Decimal
import com.productscience.data.EpochParams
import com.productscience.data.InferenceParams
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceState
import com.productscience.data.InferenceStatus
import com.productscience.data.ValidationParams
import com.productscience.data.getParticipant
import com.productscience.data.spec
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.*
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.test.assertNotNull

class InvalidationTests : TestermintTest() {
    @Test
    @Timeout(15, unit = TimeUnit.MINUTES)
    @Order(Int.MAX_VALUE - 1)
    fun `test invalid gets removed and restored`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.node.waitForNextBlock(3)

        val dispatcher = Executors.newFixedThreadPool(10).asCoroutineDispatcher()
        runBlocking(dispatcher) {
            // Each parallel inference needs a unique request to avoid duplicate inferenceIds
            // (timestamp-based IDs can collide when started at the same nanosecond)
            // Inject a unique nonce field into the JSON request
            val deferreds = (1..10).map { index ->
                async {
                    val nonce = "$index-${UUID.randomUUID()}"
                    val uniqueRequest = inferenceRequest.replaceFirst("}", ", \"_nonce\": \"$nonce\"}")
                    InferenceTestHelper(cluster, genesis, request = uniqueRequest, responsePayload = "Invalid JSON!!").runFullInference()
                }
            }
            deferreds.awaitAll()
        }

        Logger.warn("Got invalid results, waiting for invalidation.")

        genesis.markNeedsReboot()
        logSection("Waiting for removal")
        genesis.node.waitForNextBlock(5)
        val participants = genesis.api.getActiveParticipants()
        val excluded = participants.excludedParticipants.firstOrNull()
        assertNotNull(excluded, "Participant was not excluded")
        assertThat(excluded.address).isEqualTo(genesis.node.getColdAddress())
        val genesisValidatorInfo = genesis.node.getValidatorInfo()
        val validators = genesis.node.getValidators()
        assertThat(validators.validators).hasSize(3)
        val genesisValidator = validators.validators.first { it.consensusPubkey.value ==  genesisValidatorInfo.key }
        assertThat(genesisValidator.tokens).isEqualTo(0)
        genesis.waitForNextEpoch()
        val newParticipants = genesis.api.getActiveParticipants()
        assertThat(newParticipants.excludedParticipants).isEmpty()
        val removedRestored = newParticipants.activeParticipants.getParticipant(genesis)
        assertNotNull(removedRestored, "Excluded participant was not restored")
    }

    @Test
    fun `test valid with invalid validator gets validated`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate)
        genesis.waitForNextInferenceWindow()
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }
        val oddPair = cluster.joinPairs.last()
        oddPair.mock?.setInferenceResponse(defaultInferenceResponseObject.withMissingLogit())
        logSection("Getting invalid invalidation")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.node.waitForNextBlock(3)
        val invalidResult =
            generateSequence { getInferenceResult(genesis) }
                .first { it.executorBefore.id != oddPair.node.getColdAddress() }
        // The oddPair will mark it as invalid and force a vote, which should fail (valid)

        Logger.warn("Got invalid result, waiting for validation.")
        logSection("Waiting for revalidation")
        genesis.node.waitForNextBlock(10)
        logSection("Verifying revalidation")
        val newState = genesis.api.getInference(invalidResult.inference.inferenceId)

        assertThat(newState.statusEnum).isEqualTo(InferenceStatus.VALIDATED)

    }

    @Test
    fun `test invalid gets marked invalid`() {
        var tries = 3
        val (cluster, genesis) = initCluster(reboot = true)
        val oddPair = cluster.joinPairs.last()
        val badResponse = defaultInferenceResponseObject.withMissingLogit()
        oddPair.mock?.setInferenceResponse(badResponse)
        var newState: InferencePayload
        do {
            logSection("Trying to get invalid inference. Tries left: $tries")
            genesis.waitForNextInferenceWindow()
            newState = getInferenceValidationState(genesis, oddPair)
        } while (newState.statusEnum != InferenceStatus.INVALIDATED && tries-- > 0)
        logSection("Verifying invalidation")
        assertThat(newState.statusEnum).isEqualTo(InferenceStatus.INVALIDATED)
    }

    @Test
    fun `full inference with invalid response payload`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        val helper = InferenceTestHelper(cluster, genesis, responsePayload = "Invalid JSON!!")
        if (!genesis.getEpochData().safeForInference) {
            genesis.waitForStage(EpochStage.CLAIM_REWARDS, 3)
        }
        val inference = helper.runFullInference()
        // should be invalidated quickly
        genesis.node.waitForNextBlock(3)
        val inferencePayload = genesis.node.getInference(inference.inferenceId)
        assertNotNull(inferencePayload)
        assertThat(inferencePayload.inference.status).isEqualTo(InferenceStatus.INVALIDATED.value)
    }

    @Test
    fun `logprob manipulation produces different hash`() {
        // Security test: payloads with same content but different logprobs must have different hashes
        // This prevents attack where executor serves fake logprobs with valid content
        val payloadWithRealLogprobs = """{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.5,"top_logprobs":[{"token":"Hello","logprob":-0.5}]}]}}]}"""
        val payloadWithFakeLogprobs = """{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.1,"top_logprobs":[{"token":"Hello","logprob":-0.1}]}]}}]}"""

        val hash1 = computeResponseHash(payloadWithRealLogprobs)
        val hash2 = computeResponseHash(payloadWithFakeLogprobs)

        assertThat(hash1).isNotEqualTo(hash2)
    }

    companion object {
        val alwaysValidate = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::validationParams] = spec<ValidationParams> {
                        this[ValidationParams::minValidationAverage] = Decimal.fromDouble(100.0)
                        this[ValidationParams::maxValidationAverage] = Decimal.fromDouble(100.0)
                        this[ValidationParams::downtimeHThreshold] = Decimal.fromDouble(100.0)

                    }
                    this[InferenceParams::bandwidthLimitsParams] = spec<BandwidthLimitsParams> {
                        this[BandwidthLimitsParams::minimumConcurrentInvalidations] = 100L
                    }
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::inferencePruningEpochThreshold] = 100L
                        this[EpochParams::epochLength] = 20L
                        this[EpochParams::epochShift] = 15
                    }
                }
            }
        }
    }
}
