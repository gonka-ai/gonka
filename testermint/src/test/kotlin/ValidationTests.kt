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

@Timeout(value = 20, unit = TimeUnit.MINUTES)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class ValidationTests : TestermintTest() {
    @Test
    fun `test valid in parallel`() {
        val (_, genesis) = initCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 60,
                    epochShift = 40
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
    fun `late validation of inference`() {
        val (cluster, genesis) = initCluster(mergeSpec = alwaysValidate, reboot = true)
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
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.node.waitForNextBlock(3)
        val lateValidatorBeforeBalance = lateValidator.node.getSelfBalance()
        logSection("Use messages only for inference")
        val seed = lateValidator.api.getConfig().currentSeed
        val inference = helper.runFullInference()
        logSection("Wait for claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, 3)
        // Both helpers should have validated and been rewarded
        val updatedInference = genesis.node.getInference(inference.inferenceId)
        println(updatedInference)
        println(inference.inferenceId)
        println(inference.validatedBy)
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
            .take(5)
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
    val responsePayload: String = defaultInferenceResponse,
) {
    val genesisAddress = genesis.node.getColdAddress()
    
    // Phase 6: Canonicalize request to match Go's CanonicalizeJSON behavior
    // This ensures hash computation matches validator's ComputePromptHash
    val canonicalRequest: String by lazy { canonicalizeJson(request) }
    
    // Lazy initialization: timestamp is generated when first accessed (at execution time)
    // This prevents "signature is too old" errors when there are delays between
    // InferenceTestHelper construction and runFullInference() call
    val timestamp: Long by lazy { Instant.now().toEpochNanos() }
    
    // Phase 3: Dev signs hash of original_prompt (lazy to use fresh timestamp)
    val devSignature: String by lazy {
        genesis.node.signRequest(
            request,  // Use instance property, not global inferenceRequest
            accountAddress = null,
            timestamp = timestamp,
            endpointAccount = genesisAddress
        )
    }

    fun runFullInference(): InferencePayload {
        val startMessage = getStartInference()
        val response = genesis.submitMessage(startMessage)
        assertThat(response).isSuccess()

        // Store payloads BEFORE MsgFinishInference to avoid race condition:
        // Validators start retrieving payloads immediately when MsgFinishInference is confirmed,
        // so payloads must already be stored by then.
        // Phase 6: Store canonicalized request to match hash computation
        val epochId = genesis.api.getLatestEpoch().latestEpoch.index
        
        // Explicit hash verification: ensure message hashes match expected values
        // Phase 3: originalPromptHash = sha256(raw) - matches dev signature
        // Phase 6: promptHash = sha256(canonical) - what validators verify against stored payload
        val storedPromptPayload = canonicalRequest
        val expectedOriginalPromptHash = sha256(request)  // RAW hash
        val expectedPromptHash = sha256(storedPromptPayload)  // CANONICAL hash
        
        assertThat(startMessage.originalPromptHash)
            .describedAs("StartInference.originalPromptHash must match SHA256 of raw request (dev signature)")
            .isEqualTo(expectedOriginalPromptHash)
        assertThat(startMessage.promptHash)
            .describedAs("StartInference.promptHash must match SHA256 of stored canonical payload")
            .isEqualTo(expectedPromptHash)
        
        genesis.api.storePayload(
            inferenceId = devSignature,  // inferenceId = devSignature
            promptPayload = storedPromptPayload,
            responsePayload = responsePayload,
            epochId = epochId
        )

        val finishMessage = getFinishInference()
        
        // Explicit hash verification for FinishInference
        assertThat(finishMessage.originalPromptHash)
            .describedAs("FinishInference.originalPromptHash must match SHA256 of raw request (dev signature)")
            .isEqualTo(expectedOriginalPromptHash)
        assertThat(finishMessage.promptHash)
            .describedAs("FinishInference.promptHash must match SHA256 of stored canonical payload")
            .isEqualTo(expectedPromptHash)
        
        // Phase 6: Verify response hash matches computed hash from stored payload
        val expectedResponseHash = computeResponseHash(responsePayload)
        assertThat(finishMessage.responseHash)
            .describedAs("FinishInference.responseHash must match computed hash of response content")
            .isEqualTo(expectedResponseHash)
        
        val response2 = genesis.submitMessage(finishMessage)
        assertThat(response2).isSuccess()
        val inference = genesis.node.getInference(finishMessage.inferenceId)?.inference
        assertNotNull(inference)

        return inference
    }

    fun getStartInference(): MsgStartInference {
        // Phase 3: originalPromptHash = sha256(raw request) - what dev signed
        // Phase 6: promptHash = sha256(canonical request) - what validators verify
        val originalPromptHash = sha256(request)  // RAW - matches dev signature
        val promptHash = sha256(canonicalRequest)  // CANONICAL - for validator verification
        val taSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        return MsgStartInference(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptHash = promptHash,
            // promptPayload removed - Phase 6: payloads stored offchain
            model = model,
            requestedBy = genesisAddress,
            assignedTo = genesisAddress,
            nodeVersion = "",
            maxTokens = 500,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            originalPromptHash = originalPromptHash
        )
    }

    fun getFinishInference(): MsgFinishInference {
        // Phase 3: originalPromptHash = sha256(raw request) - what dev signed
        // Phase 6: promptHash = sha256(canonical request) - what validators verify
        val originalPromptHash = sha256(request)  // RAW - matches dev signature
        val promptHash = sha256(canonicalRequest)  // CANONICAL - for validator verification
        val finishTaSignature =
            genesis.node.signPayload(promptHash + timestamp.toString() + genesisAddress + genesisAddress, null)
        // Phase 6: Compute actual response hash from content (matches Go's GetHash)
        val actualResponseHash = computeResponseHash(responsePayload)
        return MsgFinishInference(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptTokenCount = 10,
            requestTimestamp = timestamp,
            transferSignature = finishTaSignature,
            responseHash = actualResponseHash,
            // responsePayload removed - Phase 6: payloads stored offchain
            completionTokenCount = 100,
            executedBy = genesisAddress,
            executorSignature = finishTaSignature,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            // originalPrompt removed - Phase 6: payloads stored offchain
            model = model,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash
        )
    }
}
