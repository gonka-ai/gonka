import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.MessageDigest
import java.time.Instant
import java.util.*
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class PoCOffChainTests : TestermintTest() {

    @Test
    fun `poc offchain artifacts - proofs endpoint and chain commits work after poc cycle`() {
        logSection("=== TEST: PoC Off-Chain Artifacts ===")

        // Initialize cluster with default configuration
        val (cluster, genesis) = initCluster(reboot = true, config = bandwidthConfig)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        // Wait for PoC generation phase to end
        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        // Wait for commit/distribution transactions to be included
        genesis.node.waitForNextBlock(3)

        val epochData = genesis.getEpochData()
        val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        // === Part 1: Query chain for store commit and weight distribution ===
        logSection("Querying chain for store commit and weight distribution")

        Logger.info("Querying for pocStartHeight=$pocStartHeight, participant=$participantAddress")

        val storeCommit = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
        Logger.info("Store commit: found=${storeCommit.found}, count=${storeCommit.count}, rootHash=${storeCommit.rootHash}")

        val weightDistribution = genesis.node.getMLNodeWeightDistribution(pocStartHeight, participantAddress)
        Logger.info("Weight distribution: found=${weightDistribution.found}, weights=${weightDistribution.weights}")

        if (storeCommit.found) {
            assertThat(storeCommit.count).isGreaterThan(0)
            assertThat(storeCommit.rootHash).isNotNull()
        }

        if (weightDistribution.found) {
            assertThat(weightDistribution.weights).isNotEmpty()
            weightDistribution.weights.forEach { weight ->
                Logger.info("Node ${weight.nodeId}: weight=${weight.weight}")
                assertThat(weight.nodeId).isNotEmpty()
            }
        }

        // === Part 2: Query DAPI artifact store and proofs ===
        logSection("Querying artifact store state from DAPI")

        val artifactState = genesis.api.getPocArtifactsState(pocStartHeight)
        Logger.info("Artifact store state: count=${artifactState.count}, rootHash=${artifactState.rootHash}")

        if (artifactState.count == 0L) {
            Logger.warn("No artifacts stored for epoch $pocStartHeight, skipping proof verification")
            logSection("TEST PASSED: Chain commits queried (no artifacts for proof test)")
            return
        }

        // === Part 3: Request and verify proofs ===
        logSection("Requesting proofs from DAPI")

        val validatorAddress = participantAddress
        val timestamp = Instant.now().toEpochNanos()
        val rootHash = artifactState.rootHash
        val count = artifactState.count
        val leafIndices = (0 until minOf(3, count.toInt())).map { it.toLong() }

        val signPayload = buildPocProofsSignPayload(
            pocStartHeight,
            Base64.getDecoder().decode(rootHash),
            count,
            leafIndices,
            timestamp,
            validatorAddress,
            validatorAddress
        )
        val signature = genesis.node.signPayload(signPayload.joinToString("") { "%02x".format(it) })

        val request = PocProofsRequest(
            pocStageStartBlockHeight = pocStartHeight,
            rootHash = rootHash,
            count = count,
            leafIndices = leafIndices,
            validatorAddress = validatorAddress,
            validatorSignerAddress = validatorAddress,
            timestamp = timestamp,
            signature = signature
        )

        val response = genesis.api.getPocProofsRaw(request)
        val statusCode = response.second.statusCode

        Logger.info("PoC proofs response status: $statusCode")
        assertThat(statusCode).isEqualTo(200)

        val proofResponse = cosmosJson.fromJson(response.third.get(), PocProofsResponse::class.java)
        assertThat(proofResponse.proofs).hasSize(leafIndices.size)

        proofResponse.proofs.forEach { proof ->
            assertThat(proof.leafIndex).isIn(*leafIndices.toTypedArray())
            assertThat(proof.vectorBytes).isNotEmpty()
            assertThat(proof.proof).isNotEmpty()
            Logger.info("Proof for leaf ${proof.leafIndex}: nonce=${proof.nonceValue}, proofLen=${proof.proof.size}")
        }

        logSection("TEST PASSED: PoC off-chain artifacts workflow complete")
    }

    @Test
    fun `poc offchain validation - query all store commits for stage`() {
        logSection("=== TEST: PoC Off-Chain Validation - All Store Commits Query ===")

        // Initialize cluster
        val (cluster, genesis) = initCluster(reboot = true, config = bandwidthConfig)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        // Wait for PoC generation to complete and some artifacts to be generated
        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val epochData = genesis.getEpochData()
        val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        // First verify individual store commit works
        logSection("Verifying individual store commit query")
        val storeCommit = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
        Logger.info("Individual store commit: found=${storeCommit.found}, count=${storeCommit.count}")

        // Query all store commits for the stage using the new endpoint
        logSection("Querying all store commits for stage using new endpoint")
        val allCommits = genesis.node.getAllPoCV2StoreCommitsForStage(pocStartHeight)
        Logger.info("All commits for stage: ${allCommits.commits.size} participants")

        // Verify results
        if (storeCommit.found) {
            // If we have an individual commit, it should appear in all commits
            assertThat(allCommits.commits).isNotEmpty()
            
            // Find our participant in the list
            val ourCommit = allCommits.commits.find { it.participantAddress == participantAddress }
            if (ourCommit != null) {
                Logger.info("Found our commit in all commits: count=${ourCommit.count}")
                assertThat(ourCommit.count).isEqualTo(storeCommit.count)
            } else {
                Logger.warn("Our participant not found in all commits (may have been filtered)")
            }
        }

        allCommits.commits.forEach { commit ->
            Logger.info("Commit: participant=${commit.participantAddress}, count=${commit.count}")
            assertThat(commit.participantAddress).isNotEmpty()
            assertThat(commit.count).isGreaterThanOrEqualTo(0)
        }

        logSection("TEST PASSED: All store commits query works correctly")
    }

    companion object {
        /**
         * Builds the binary payload for PoC proofs signature verification.
         * Format: SHA256(poc_stage_start_block_height(LE64) || root_hash(32) || count(LE32) ||
         *         leaf_indices(LE32 each) || timestamp(LE64) || validator_address || validator_signer_address)
         */
        fun buildPocProofsSignPayload(
            pocStageStartBlockHeight: Long,
            rootHash: ByteArray,
            count: Long,
            leafIndices: List<Long>,
            timestamp: Long,
            validatorAddress: String,
            validatorSignerAddress: String
        ): ByteArray {
            // Calculate buffer size
            val size = 8 + 32 + 4 + (leafIndices.size * 4) + 8 +
                    validatorAddress.toByteArray().size + validatorSignerAddress.toByteArray().size

            val buffer = ByteBuffer.allocate(size)
            buffer.order(ByteOrder.LITTLE_ENDIAN)

            buffer.putLong(pocStageStartBlockHeight)
            buffer.put(rootHash)
            buffer.putInt(count.toInt())
            leafIndices.forEach { buffer.putInt(it.toInt()) }
            buffer.putLong(timestamp)
            buffer.put(validatorAddress.toByteArray())
            buffer.put(validatorSignerAddress.toByteArray())

            // SHA256 hash
            val digest = MessageDigest.getInstance("SHA-256")
            return digest.digest(buffer.array())
        }
    }

    val offChainPoCSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::pocStageDuration] = 3L
                    this[EpochParams::pocValidationDuration] = 4L
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = true
                }
            }
        }
    }

    val bandwidthConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(offChainPoCSpec) ?: offChainPoCSpec,
    )
}
