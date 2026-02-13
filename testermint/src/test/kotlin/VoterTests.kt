import com.productscience.*
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
        genesis.node.waitForNextBlock(4)
        val inference = genesis.node.getInference(inferenceResponse.id)?.inference
        assertNotNull(inference)

        assertThat(inference.inferenceId).isEqualTo(inferenceSignature)
        assertThat(inference.requestTimestamp).isEqualTo(inferenceTimestamp)
        assertThat(inference.transferredBy).isEqualTo(genesisAddress)
        // The inference should be at least FINISHED — the voter successfully recovered the payload.
        // It may have already progressed to VALIDATED by the time we check.
        assertThat(inference.status).isGreaterThanOrEqualTo(InferenceStatus.FINISHED.value)
        assertThat(inference.executedBy).isEqualTo(inference.assignedTo)
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
        genesis.node.waitForNextBlock(4)
        val inference = genesis.node.getInference(inferenceResponse.id)?.inference
        assertNotNull(inference)

        assertThat(inference.inferenceId).isEqualTo(inferenceSignature)
        assertThat(inference.requestTimestamp).isEqualTo(inferenceTimestamp)
        assertThat(inference.transferredBy).isEqualTo(genesisAddress)
        // The inference should still be STARTED — no one could execute it
        assertThat(inference.status).isEqualTo(InferenceStatus.STARTED.value)
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
