import com.productscience.*
import com.productscience.assertions.assertThat
import com.productscience.data.*
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.*
import org.tinylog.kotlin.Logger
import java.time.Instant
import java.util.*
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.test.assertNotNull

@Timeout(value = 15, unit = TimeUnit.MINUTES)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class ValidationTests : TestermintTest() {
    @Test
    fun `test valid in parallel`() {
        val (_, genesis) = initCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                ),
            ),
            reboot = true,
            mergeSpec = ignoreDowntime
        )

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 3)
        logSection("Making inference requests in parallel")
        val requests = 50
        val inferenceRequest = inferenceRequestObject.copy(
            maxTokens = 20 // To not trigger bandwidth limit
        )
        val statuses = runParallelInferences(
            genesis, requests, maxConcurrentRequests = requests,
            inferenceRequest = inferenceRequest
        )
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
    fun `test invalid gets marked invalid`() {
        var tries = 3
        val (cluster, genesis) = initCluster(reboot = true)
        genesis.waitForNextInferenceWindow(10)
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
    @Order(Int.MAX_VALUE - 1)
    fun `test invalid gets removed and restored`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }
        genesis.waitForNextInferenceWindow()

        val dispatcher = Executors.newFixedThreadPool(10).asCoroutineDispatcher()
        runBlocking(dispatcher) {
        val deferreds = (1..10).map {
                async {
            InferenceTestHelper(cluster, genesis, responsePayload = "Invalid JSON!!").runFullInference()
                }
            }
            deferreds.awaitAll()
        }

        Logger.warn("Got invalid results, waiting for invalidation.")

        genesis.markNeedsReboot()
        logSection("Waiting for removal")
        genesis.node.waitForNextBlock(2)
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
    fun `late validation of inference`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate)
        genesis.waitForNextEpoch()
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }
        val helper = InferenceTestHelper(cluster, genesis)
        val lateValidator = cluster.joinPairs.first()
        val mlNodeVersionResponse = genesis.node.getMlNodeVersion()
        val mlNodeVersion = mlNodeVersionResponse.mlnodeVersion.currentVersion
        val segment = "/${mlNodeVersion}"
        lateValidator.mock?.setInferenceErrorResponse(500, segment = segment)
        logSection("Make sure we're in safe inference zone")
        if (!genesis.getEpochData().safeForInference) {
            genesis.waitForStage(EpochStage.CLAIM_REWARDS, 3)
        }
        val lateValidatorBeforeBalance = lateValidator.node.getSelfBalance()
        logSection("Use messages only for inference")
        val seed = lateValidator.api.getConfig().currentSeed
        val inference = helper.runFullInference()
        logSection("Wait for claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, 3)
        // Both helpers should have validated and been rewarded
        val updatedInference = genesis.node.getInference(inference.inferenceId)
        // Only the other join should have validated
        assertNotNull(updatedInference)
        assertNotNull(updatedInference.inference)

        assertThat(
            updatedInference.inference.validatedBy ?: listOf()
        ).doesNotContain(lateValidator.node.getColdAddress())
        val afterBalance = lateValidator.node.getSelfBalance()
        assertThat(afterBalance).isEqualTo(lateValidatorBeforeBalance)
        logSection("Submit late validation")
        val beforeCoinsOwed =
            lateValidator.api.getParticipants().first { it.id == lateValidator.node.getColdAddress() }.coinsOwed
        val validationMessage = MsgValidation(
            id = UUID.randomUUID().toString(),
            inferenceId = inference.inferenceId,
            creator = lateValidator.node.getColdAddress(),
            value = 1.0
        )

        val validation = lateValidator.submitMessage(validationMessage)
        assertThat(validation).isSuccess()
        val afterCoinsOwed =
            lateValidator.api.getParticipants().first { it.id == lateValidator.node.getColdAddress() }.coinsOwed
        assertThat(afterCoinsOwed).isEqualTo(beforeCoinsOwed)
        val beforeClaimBalance = lateValidator.node.getSelfBalance()
        // And now reclaim:
        val claim = MsgClaimRewards(
            creator = lateValidator.node.getColdAddress(),
            seed = seed.seed,
            epochIndex = seed.epochIndex,
        )
        val reclaim = lateValidator.submitMessage(claim)
        assertThat(reclaim).isSuccess()
        val afterClaimBalance = lateValidator.node.getSelfBalance()
        assertThat(afterClaimBalance).isGreaterThan(beforeClaimBalance)
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
                        // need longer epochs to have time for invalidations
//                        this[EpochParams::epochLength] = 20L
                    }
                }
            }
        }

        val ignoreDowntime = spec {
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
                    }
                }
            }
        }
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
                it.executorBefore.id == oddPair.node.getColdAddress()
            }
    if (invalidResult == null) {
        error("Did not get result from invalid pair(${oddPair.node.getColdAddress()}) in time")
    }

    Logger.warn(
        "Got invalid result, waiting for invalidation. " +
                "Output was:${invalidResult.inference.responsePayload}"
    )

    highestFunded.node.waitForNextBlock(3)
    val newState = highestFunded.api.getInference(invalidResult.inference.inferenceId)
    return newState
}

data class InferenceTestHelper(
    val cluster: LocalCluster,
    val genesis: LocalInferencePair,
    val request: String = inferenceRequest,
    val model: String = defaultModel,
    val promptHash: String = "not_verified",
    val timestamp: Long = Instant.now().toEpochNanos(),
    val responsePayload: String = defaultInferenceResponse,
) {
    val genesisAddress = genesis.node.getColdAddress()
    val devSignature = genesis.node.signPayload(
        inferenceRequest,
        accountAddress = null,
        timestamp = timestamp,
        endpointAccount = genesisAddress
    )

    fun runFullInference(): InferencePayload {
        val startMessage = getStartInference()
        val response = genesis.submitMessage(startMessage)
        assertThat(response).isSuccess()
        val finishMessage = getFinishInference()
        val response2 = genesis.submitMessage(finishMessage)
        assertThat(response2).isSuccess()
        val inference = genesis.node.getInference(finishMessage.inferenceId)?.inference
        assertNotNull(inference)
        return inference
    }

    fun getStartInference(): MsgStartInference {
        val taSignature =
            genesis.node.signPayload(request + timestamp.toString() + genesisAddress + genesisAddress, null)
        return MsgStartInference(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptHash = "not_verified",
            promptPayload = request,
            model = model,
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature
        )
    }

    fun getFinishInference(): MsgFinishInference {
        val finishTaSignature =
            genesis.node.signPayload(request + timestamp.toString() + genesisAddress + genesisAddress, null)
        return MsgFinishInference(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = finishTaSignature,
            responseHash = "fjdsf",
            responsePayload = responsePayload,
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = finishTaSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            originalPrompt = request,
            model = model
        )
    }
}
