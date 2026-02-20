import com.productscience.*
import com.productscience.assertions.assertThat as assertTxThat
import com.productscience.data.*
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
        // When voter recovers the payload, executor runs inference normally → status FINISHED.
        var inference: InferencePayload? = waitInferenceUntilStatus(InferenceStatus.FINISHED, inferenceResponse.id, maxTries = 60)
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

        // Wait until validated. Use CLAIM_REWARDS (one stage after SET_NEW_VALIDATORS)
        // so validation has time to run; increase tries for polling.
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        inference = waitInferenceUntilStatus(InferenceStatus.VALIDATED, inferenceResponse.id, maxTries = 30)
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
     * 4. Executor submits MsgFinishInferenceWithMissingPayload with negative votes → status FINISHED_WITH_MISSING_PAYLOAD (refund).
     */
    @Test
    fun `all voters cast negative votes when payload does not exist`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
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
        // Executor submits MsgFinishInferenceWithMissingPayload with negative outcome → status FINISHED_WITH_MISSING_PAYLOAD.
        var inference: InferencePayload? = waitInferenceUntilStatus(
            InferenceStatus.FINISHED_WITH_MISSING_PAYLOAD,
            inferenceResponse.id,
            maxTries = 30,
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

    /**
     * Case #4: Chain rejects MsgFinishInferenceWithMissingPayload with empty votes.
     *
     * Submits a MsgFinishInferenceWithMissingPayload where the VotingResult has no votes.
     * The on-chain validation should reject this with "invalid vote count".
     */
    @Test
    fun `chain rejects finish with missing payload when votes are empty`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val devSignature = genesis.node.signPayload(
            originalPromptHash + timestamp.toString() + genesisAddress, null
        )
        val taSignature = genesis.node.signPayload(
            promptHash + timestamp.toString() + genesisAddress + genesisAddress, null
        )

        val finishMsg = FinishInferenceData(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptTokenCount = 10,
            completionTokenCount = 100,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            executorSignature = taSignature,
            responseHash = "fake-response-hash",
            executedBy = genesisAddress,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash,
        )

        val emptyVotingResult = VotingResult(
            inferenceId = devSignature,
            votes = emptyList(),
            completedAt = Instant.now().toEpochNanos(),
            requesterAddress = genesisAddress,
            requesterSignature = "",
        )

        val message = MsgFinishInferenceWithMissingPayload(
            creator = genesisAddress,
            msgFinishInference = finishMsg,
            votingResult = emptyVotingResult,
        )

        val response = genesis.submitMessage(message)
        assertTxThat(response).isFailure()
        assertThat(response.rawLog).contains("invalid vote count")
    }

    /**
     * Case #5: Chain rejects MsgFinishInferenceWithMissingPayload with a forged requester signature.
     *
     * Submits a MsgFinishInferenceWithMissingPayload where the VotingResult has votes
     * but the requester_signature is fabricated. The on-chain validation should reject this.
     */
    @Test
    fun `chain rejects finish with missing payload when requester signature is forged`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val devSignature = genesis.node.signPayload(
            originalPromptHash + timestamp.toString() + genesisAddress, null
        )
        val taSignature = genesis.node.signPayload(
            promptHash + timestamp.toString() + genesisAddress + genesisAddress, null
        )

        val finishMsg = FinishInferenceData(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptTokenCount = 10,
            completionTokenCount = 100,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            executorSignature = taSignature,
            responseHash = "fake-response-hash",
            executedBy = genesisAddress,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash,
        )

        val voteTimestamp = Instant.now().toEpochNanos()
        val forgedVotingResult = VotingResult(
            inferenceId = devSignature,
            votes = listOf(
                SignedVote(
                    inferenceId = devSignature,
                    voterAddress = genesisAddress,
                    voteType = 2, // VoteNegative
                    respondentDataHash = "",
                    timestamp = voteTimestamp,
                    voterSignature = genesis.node.signVote(
                        devSignature,
                        genesisAddress,
                        2,
                        "",
                        voteTimestamp,
                        genesisAddress,
                    ),
                ),
            ),
            completedAt = Instant.now().toEpochNanos(),
            requesterAddress = genesisAddress,
            requesterSignature = "forged-signature-not-valid-base64",
        )

        val message = MsgFinishInferenceWithMissingPayload(
            creator = genesisAddress,
            msgFinishInference = finishMsg,
            votingResult = forgedVotingResult,
        )

        val response = genesis.submitMessage(message)
        assertTxThat(response).isFailure()
        assertThat(response.rawLog).contains("invalid voting result signature")
    }

    /**
     * Case #6: Chain rejects MsgFinishInferenceWithMissingPayload with mismatched inference IDs.
     *
     * The VotingResult's inference_id doesn't match MsgFinishInference's inference_id.
     */
    @Test
    fun `chain rejects finish with missing payload when inference IDs mismatch`() {
        val timestamp = Instant.now().toEpochNanos()
        val genesisAddress = genesis.node.getColdAddress()
        val originalPromptHash = sha256(inferenceRequest)
        val promptHash = originalPromptHash
        val devSignature = genesis.node.signPayload(
            originalPromptHash + timestamp.toString() + genesisAddress, null
        )
        val taSignature = genesis.node.signPayload(
            promptHash + timestamp.toString() + genesisAddress + genesisAddress, null
        )

        val finishMsg = FinishInferenceData(
            creator = genesisAddress,
            inferenceId = devSignature,
            promptTokenCount = 10,
            completionTokenCount = 100,
            requestTimestamp = timestamp,
            transferSignature = taSignature,
            executorSignature = taSignature,
            responseHash = "fake-response-hash",
            executedBy = genesisAddress,
            transferredBy = genesisAddress,
            requestedBy = genesisAddress,
            model = defaultModel,
            promptHash = promptHash,
            originalPromptHash = originalPromptHash,
        )

        val voteTimestamp = Instant.now().toEpochNanos()
        val mismatchedVotes = listOf(
            SignedVote(
                inferenceId = "different-inference-id",
                voterAddress = genesisAddress,
                voteType = 2,
                respondentDataHash = "",
                timestamp = voteTimestamp,
                voterSignature = genesis.node.signVote(
                    "different-inference-id",
                    genesisAddress,
                    2,
                    "",
                    voteTimestamp,
                    genesisAddress,
                ),
            ),
        )
        val completedAt = Instant.now().toEpochNanos()
        val mismatchedVotingResult = VotingResult(
            inferenceId = "different-inference-id",
            votes = mismatchedVotes,
            completedAt = completedAt,
            requesterAddress = genesisAddress,
            requesterSignature = genesis.node.signVotingResult(
                "different-inference-id",
                mismatchedVotes,
                completedAt,
                genesisAddress,
            ),
        )

        val message = MsgFinishInferenceWithMissingPayload(
            creator = genesisAddress,
            msgFinishInference = finishMsg,
            votingResult = mismatchedVotingResult,
        )

        val response = genesis.submitMessage(message)
        assertTxThat(response).isFailure()
        assertThat(response.rawLog).contains("inference IDs are different")
    }

    /**
     * Case #7: On-chain validation fails with dishonest sampled voters.
     *
     * When the executor submits MsgFinishInferenceWithMissingPayload with a VotingResult that
     * includes voters not in the replayable sampled set (e.g. TA or executor as voter),
     * the chain rejects it with "not in replayable sampled set".
     *
     * Flow:
     * 1. TA sends MsgStartInference via API (VOTER_NEGATIVE_SEED - payload not stored).
     * 2. Wait for inference to appear on chain in STARTED status (MsgStartInference processed).
     * 3. Executor maliciously submits MsgFinishInferenceWithMissingPayload with TA as voter
     *    (TA is excluded from sampling).
     * 4. Chain rejects with voter legitimacy error.
     */
    @Test
    fun `on chain validation fails with dishonest sampled voters`() {
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

        // Wait for MsgStartInference to be on chain (STARTED status with assignedTo).
        var inference: InferencePayload? = waitInferenceUntilStatus(InferenceStatus.STARTED, inferenceResponse.id, maxTries = 15)
        assertNotNull(inference)
        val assignedTo = inference!!.assignedTo
        assertNotNull(assignedTo) { "Inference should have assignedTo (executor)" }
        val transferredBy = inference.transferredBy
        assertNotNull(transferredBy) { "Inference should have transferredBy (TA)" }

        val executorPair = cluster.allPairs.firstOrNull { it.node.getColdAddress() == assignedTo }
            ?: error("Executor pair not found for address $assignedTo")

        // Build malicious VotingResult with TA as voter - TA is excluded from sampling.
        val completedAt = Instant.now().toEpochNanos()
        val taPair = cluster.allPairs.firstOrNull { it.node.getColdAddress() == transferredBy }
            ?: error("TA pair not found for address $transferredBy")
        val dishonestVotes = listOf(
            SignedVote(
                inferenceId = inference.inferenceId,
                voterAddress = transferredBy, // TA - excluded from sampling!
                voteType = 2, // VoteNegative
                respondentDataHash = "",
                timestamp = completedAt,
                voterSignature = taPair.node.signVote(
                    inference.inferenceId,
                    transferredBy,
                    2,
                    "",
                    completedAt,
                    transferredBy,
                ),
            ),
        )
        val requesterSignature = executorPair.node.signVotingResult(
            inferenceId = inference.inferenceId,
            votes = dishonestVotes,
            completedAt = completedAt,
            requesterAddress = assignedTo,
        )

        val maliciousVotingResult = VotingResult(
            inferenceId = inference.inferenceId,
            votes = dishonestVotes,
            completedAt = completedAt,
            requesterAddress = assignedTo,
            requesterSignature = requesterSignature,
        )

        val finishMsg = FinishInferenceData(
            creator = assignedTo,
            inferenceId = inference.inferenceId,
            promptTokenCount = 10,
            completionTokenCount = 100,
            requestTimestamp = inference.requestTimestamp ?: inferenceTimestamp,
            transferSignature = inference.transferSignature ?: "",
            executorSignature = inference.executionSignature ?: "",
            responseHash = "fake-response-hash",
            executedBy = assignedTo,
            transferredBy = transferredBy,
            requestedBy = inference.requestedBy ?: genesisAddress,
            model = inference.model ?: defaultModel,
            promptHash = inference.promptHash,
            originalPromptHash = inference.originalPromptHash ?: inference.promptHash,
        )

        val message = MsgFinishInferenceWithMissingPayload(
            creator = assignedTo,
            msgFinishInference = finishMsg,
            votingResult = maliciousVotingResult,
        )

        val response = executorPair.submitMessage(message)
        assertTxThat(response).isFailure()
        assertThat(response.rawLog).contains("not in replayable sampled set")
    }

    /**
     * Case #8: Chain rejects MsgFinishInferenceWithMissingPayload when executor forges voter signature.
     *
     * The executor submits a vote claiming it's from a legitimate sampled voter, but the signature
     * is forged (signed by the executor's key instead of the voter's). The chain validates each
     * voter signature against the voter's pubkey and rejects with "invalid voter signature".
     *
     * Flow:
     * 1. TA sends MsgStartInference via API (VOTER_NEGATIVE_SEED - payload not stored).
     * 2. Wait for inference to appear on chain in STARTED status.
     * 3. Executor maliciously submits MsgFinishInferenceWithMissingPayload with a vote that
     *    claims to be from a legitimate voter but uses executor's signature (forged).
     * 4. Chain rejects with "invalid voter signature".
     */
    @Test
    fun `chain rejects finish with missing payload when executor forges voter signature`() {
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
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

        var inference: InferencePayload? = waitInferenceUntilStatus(InferenceStatus.STARTED, inferenceResponse.id, maxTries = 15)
        assertNotNull(inference)
        val assignedTo = inference!!.assignedTo
        assertNotNull(assignedTo) { "Inference should have assignedTo (executor)" }
        val transferredBy = inference.transferredBy
        assertNotNull(transferredBy) { "Inference should have transferredBy (TA)" }

        val executorPair = cluster.allPairs.firstOrNull { it.node.getColdAddress() == assignedTo }
            ?: error("Executor pair not found for address $assignedTo")

        // Pick a legitimate voter: any participant that is neither TA nor executor.
        val voterPair = cluster.allPairs.firstOrNull {
            val addr = it.node.getColdAddress()
            addr != assignedTo && addr != transferredBy
        } ?: error("No voter pair found (need at least 3 nodes)")

        val voterAddress = voterPair.node.getColdAddress()
        val completedAt = Instant.now().toEpochNanos()

        // Forged vote: claim voterAddress voted, but sign with executor's key.
        val forgedVotes = listOf(
            SignedVote(
                inferenceId = inference.inferenceId,
                voterAddress = voterAddress,
                voteType = 2, // VoteNegative
                respondentDataHash = "",
                timestamp = completedAt,
                voterSignature = executorPair.node.signVote(
                    inference.inferenceId,
                    voterAddress,
                    2,
                    "",
                    completedAt,
                    assignedTo, // executor signs with their key, not voter's
                ),
            ),
        )

        val requesterSignature = executorPair.node.signVotingResult(
            inferenceId = inference.inferenceId,
            votes = forgedVotes,
            completedAt = completedAt,
            requesterAddress = assignedTo,
        )

        val forgedVotingResult = VotingResult(
            inferenceId = inference.inferenceId,
            votes = forgedVotes,
            completedAt = completedAt,
            requesterAddress = assignedTo,
            requesterSignature = requesterSignature,
        )

        val finishMsg = FinishInferenceData(
            creator = assignedTo,
            inferenceId = inference.inferenceId,
            promptTokenCount = 10,
            completionTokenCount = 100,
            requestTimestamp = inference.requestTimestamp ?: inferenceTimestamp,
            transferSignature = inference.transferSignature ?: "",
            executorSignature = inference.executionSignature ?: "",
            responseHash = "fake-response-hash",
            executedBy = assignedTo,
            transferredBy = transferredBy,
            requestedBy = inference.requestedBy ?: genesisAddress,
            model = inference.model ?: defaultModel,
            promptHash = inference.promptHash,
            originalPromptHash = inference.originalPromptHash ?: inference.promptHash,
        )

        val message = MsgFinishInferenceWithMissingPayload(
            creator = assignedTo,
            msgFinishInference = finishMsg,
            votingResult = forgedVotingResult,
        )

        val response = executorPair.submitMessage(message)
        assertTxThat(response).isFailure()
        assertThat(response.rawLog).contains("invalid voter signature")
    }

    fun waitInferenceUntilStatus(
        expectedStatus: InferenceStatus,
        inferenceId: String,
        maxTries: Int = 5,
    ): InferencePayload? {
        return waitUntilStatus(expectedStatus, maxTries) {
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
        maxTries: Int = 5,
        getStatus: () -> Pair<T, InferenceStatus>?,
    ): T? {
        var tries = maxTries
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
