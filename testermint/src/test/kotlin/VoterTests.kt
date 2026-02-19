import com.productscience.*
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.Test
import java.time.Instant
import kotlin.test.assertNotNull

/**
 * Tests for the voter fallback mechanism.
 *
 * When a TA sends MsgStartInference on chain but the executor never receives the payload directly,
 * the executor falls back to sampling voters who attempt to retrieve the payload from the TA on
 * the executor's behalf.
 */
class VoterTests : TestermintTest() {
    /**
     * Case #1: TA stores payload but executor's direct retrieval is blocked.
     *
     * Flow:
     * 1. TA sends MsgStartInference on chain and stores the payload in prompt storage,
     *    but never forwards the payload to the executor.
     * 2. Executor detects MsgStartInference on chain, tries to retrieve the payload
     *    directly from the TA — but this is blocked (simulated failure via VoterRecoverySeed).
     * 3. Executor falls back to voters. The first voter pings the TA's prompt endpoint,
     *    successfully retrieves the payload, and forwards it back to the executor.
     * 4. Executor processes the inference normally — it should reach FINISHED status.
     */
    @Test
    fun `voter recovers payload from TA when executor direct retrieval fails`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val initialBalance = genesis.node.getSelfBalance()

        val inferenceTimestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val inferenceSignature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = inferenceTimestamp,
            endpointAccount = genesisAddress,
        )
        val inferenceResponse = genesis.api.makeInferenceRequest(
            inferenceRequest,
            genesisAddress,
            inferenceSignature,
            inferenceTimestamp,
            seed = VOTER_RECOVERY_SEED,
        )

        // Wait for the voter fallback and inference execution to complete.
        // The flow is: executor detects MsgStart → direct retrieval fails →
        // voter fallback → voter gets payload from TA → forwards to executor → executor finishes.
        var inference: InferencePayload? = waitInferenceUntilStatus(InferenceStatus.FINISHED, inferenceResponse.id)
        assertNotNull(inference)

        val finalBalance = genesis.node.getSelfBalance()

        assertThat(inference.inferenceId).isEqualTo(inferenceSignature)
        assertThat(inference.requestTimestamp).isEqualTo(inferenceTimestamp)
        assertThat(inference.transferredBy).isEqualTo(genesisAddress)
        assertThat(inference.status).isEqualTo(InferenceStatus.FINISHED.value)
        assertThat(inference.executedBy).isEqualTo(inference.assignedTo)
        // No refund given
        assertThat(inference.actualCost).isGreaterThan(0)
        assertThat(initialBalance - finalBalance).isEqualTo(inference.actualCost)

        // Wait until validated (although it might not have completed by then)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        inference = waitInferenceUntilStatus(InferenceStatus.VALIDATED, inferenceResponse.id)
        assertNotNull(inference)
        assertThat(inference.status).matches { it == InferenceStatus.VALIDATED.value || it == InferenceStatus.FINISHED.value }
    }

    /**
     * Case #2: TA never stores the payload - voters also can't find it.
     *
     * Flow:
     * 1. TA sends MsgStartInference on chain but does NOT store the payload in prompt storage
     *    and does NOT forward it to the executor.
     * 2. Executor detects MsgStartInference on chain, tries to retrieve the payload
     *    directly from the TA — fails (payload doesn't exist).
     * 3. Executor falls back to voters. All voters ping the TA's prompt endpoint,
     *    but the payload is not there, so they all cast negative votes.
     * 4. The inference remains in STARTED status — it was never executed.
     */
    @Test
    fun `all voters cast negative votes when payload does not exist`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val initialBalance = genesis.node.getSelfBalance()

        val inferenceTimestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val inferenceSignature = genesis.node.signRequest(
            inferenceRequest,
            accountAddress = null,
            timestamp = inferenceTimestamp,
            endpointAccount = genesisAddress,
        )
        val inferenceResponse = genesis.api.makeInferenceRequest(
            inferenceRequest,
            genesisAddress,
            inferenceSignature,
            inferenceTimestamp,
            seed = VOTER_NEGATIVE_SEED,
        )

        // Wait enough blocks for the voter fallback to complete.
        // All voters should fail to find the payload and cast negative votes.
        var inference: InferencePayload? = waitInferenceUntilStatus(
            InferenceStatus.FINISHED_WITH_MISSING_PAYLOAD,
            inferenceResponse.id,
        )
        assertNotNull(inference)

        val finalBalance = genesis.node.getSelfBalance()

        assertThat(inference.inferenceId).isEqualTo(inferenceSignature)
        assertThat(inference.requestTimestamp).isEqualTo(inferenceTimestamp)
        assertThat(inference.transferredBy).isEqualTo(genesisAddress)
        assertThat(inference.status).isEqualTo(InferenceStatus.FINISHED_WITH_MISSING_PAYLOAD.value)
        // Refund given
        assertThat(inference.actualCost).isNull()
        assertThat(finalBalance).isEqualTo(initialBalance)

        // Ensure we remain finished with missing payload
        inference = waitInferenceUntilStatus(InferenceStatus.VALIDATED, inferenceResponse.id)
        assertNotNull(inference)
        assertThat(inference.status).isEqualTo(InferenceStatus.FINISHED_WITH_MISSING_PAYLOAD.value)
    }

    /**
     * Case #3: Voter rejects verification request when MsgStartInference does not exist.
     *
     * Flow:
     * 1. A challenger sends a POST /v1/voting/verify with a fabricated inference ID
     *    that has no corresponding MsgStartInference on chain.
     * 2. The voter queries the chain, finds no inference, and rejects the request
     *    with HTTP 400 "inference not found on chain".
     */
    @Test
    fun `voter rejects verification when MsgStartInference does not exist`() {
        // Pick any node as the voter target (use a join node so it's not the TA)
        val voter = cluster.joinPairs.first()

        // Use a completely fabricated inference ID that doesn't exist on chain
        val fakeInferenceId = "nonexistent-inference-id-12345"
        val genesisAddress = genesis.node.getColdAddress()
        val genesisUrl = genesis.api.getPublicUrl()

        val (_, response, _) = voter.api.makeVotingVerifyRequest(
            inferenceId = fakeInferenceId,
            respondentAddress = genesisAddress,
            respondentUrl = genesisUrl,
        )

        // The voter should reject this with 400 because no MsgStartInference exists
        assertThat(response.statusCode).isEqualTo(400)
        val body = String(response.data)
        assertThat(body).contains("inference not found on chain")
    }

    fun waitInferenceUntilStatus(expectedStatus: InferenceStatus, inferenceId: String): InferencePayload? {
        return waitUntilStatus(expectedStatus) {
            val inference = genesis.node.getInference(inferenceId)?.inference
            if (inference != null) {
                Pair(inference, InferenceStatus.values()[inference.status])
            } else {
                null
            }
        }
    }

    fun <T> waitUntilStatus(
        expectedStatus: InferenceStatus,
        getStatus: () -> Pair<T, InferenceStatus>?,
    ): T? {
        var tries = 5
        var newStatus: Pair<T, InferenceStatus>?
        do {
            logSection("Trying to get inference with status. Tries left: $tries.")
            genesis.node.waitForNextBlock(1)
            newStatus = getStatus()
        } while (newStatus?.second != expectedStatus && tries-- > 0)
        return newStatus?.first
    }

    companion object {
        // Must match VoterRecoverySeed in post_chat_handler.go
        const val VOTER_RECOVERY_SEED = 3141592
        // Must match VoterNegativeSeed in post_chat_handler.go
        const val VOTER_NEGATIVE_SEED = 2718281

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
