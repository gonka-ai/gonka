import com.productscience.*
import com.productscience.assertions.assertThat
import com.productscience.data.getParticipant
import com.github.kittinunf.fuel.core.FuelError
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class NodeDisableInferenceTests : TestermintTest() {

    @Test
    fun `test node disable inference default state`() {
        // 1. Setup genesis with 2 ML nodes
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName = mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose-local-mock-node-2.yml")
            ),
            nodeConfigFileByKeyName = mapOf(
                GENESIS_KEY_NAME to "node_payload_mock-server_genesis_2_nodes.json"
            ),
            genesisSpec = createSpec(
                epochLength = 25,
                epochShift = 10
            ),
        )
        // We need 3 participants: Genesis + 2 Joiners (default initCluster provides Genesis + 2 Joiners)
        val (cluster, genesis) = initCluster(config = config, reboot = true, resetMlNodes = false)
        // 2. Verify active participants and Genesis ML nodes
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val participants = genesis.api.getActiveParticipants().activeParticipants
        assertThat(participants.participants).hasSize(3)

        val genesisRegisteredNodes = genesis.api.getNodes()
        assertThat(genesisRegisteredNodes).hasSize(2)

        val genesisParticipant = participants.getParticipant(genesis)
        assertThat(genesisParticipant).isNotNull

        // 3. Wait for INFERENCE phase and disable join-1
        logSection("Waiting for Inference Window")
        genesis.waitForNextInferenceWindow()

        val join1 = cluster.joinPairs[0]
        logSection("Disabling join-1")
        join1.api.getNodes()
            .first()
            .also { n ->
                val nodeId = n.node.id
                val disableResponse = join1.api.disableNode(n.node.id)
                assertThat(disableResponse.nodeId).isEqualTo(nodeId)
            }

        // 4. The disable should not affect the current epoch immediately.
        // Make sure join-1 still serves at least one inference in this epoch and can later claim for it.
        val rewardSeed = join1.api.getConfig().currentSeed
        logSection("Waiting for an inference assigned to disabled join-1 in the current epoch")
        val join1Address = join1.node.getColdAddress()
        val earnedInference = (1..20)
            .asSequence()
            .mapNotNull { attempt ->
                runCatching { getInferenceResult(genesis) }
                    .onFailure { error ->
                        val isTemporary500 = error is FuelError &&
                            error.message.orEmpty().contains("500 Internal Server Error")
                        val isChainLag = error is IllegalStateException &&
                            error.message.orEmpty().contains("Inference never logged in chain")
                        if (!isTemporary500 && !isChainLag) {
                            throw error
                        }
                        Logger.info(
                            "Inference attempt $attempt hit a transient response while waiting for join-1 assignment; retrying on the next block: ${error.message}"
                        )
                        genesis.waitForBlock(1) { true }
                    }
                    .getOrNull()
            }
            .firstOrNull { result ->
                result.inference.assignedTo == join1Address || result.inference.executedBy == join1Address
            }
            ?: error("Disabled join-1 did not receive an inference in the current epoch")

        assertThat(earnedInference.inference.assignedTo).isEqualTo(join1Address)
        logSection("join-1 served inference ${earnedInference.inference.inferenceId} after disable")

        genesis.markNeedsReboot()
        // Stop join-1 API so automatic reward recovery does not claim before the manual claim below.
        join1.stopApiContainer()
        logSection("Stopped join1-api to prevent auto-claim before manual verification")

        // 5. Wait for claim rewards and verify join-1 can still claim rewards earned before disable took effect.
        val claimWindow = genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        logSection("Attempting to claim rewards for join-1. claimWindow = ${claimWindow.stageBlock}")

        val initialBalance = join1.node.getSelfBalance()
        logSection("Join-1 Balance before claim: $initialBalance")

        val claimResponse = join1.submitTransaction(
            listOf(
                "inference",
                "claim-rewards",
                rewardSeed.seed.toString(),
                rewardSeed.epochIndex.toString(),
            )
        )
        assertThat(claimResponse).isSuccess()

        val finalBalance = join1.node.getSelfBalance()
        logSection("Join-1 Balance after claim: $finalBalance")

        assertThat(finalBalance).isGreaterThan(initialBalance)
        Logger.info("Join-1 successfully claimed rewards after being disabled.")
    }
}
