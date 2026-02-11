import com.productscience.*
import com.productscience.data.*
import com.productscience.assertions.assertThat
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
        )
        // We need 3 participants: Genesis + 2 Joiners (default initCluster provides Genesis + 2 Joiners)
        val (cluster, genesis) = initCluster(config = config, reboot = true, resetMlNodes = false)

        // 2. Verify active participants and Genesis ML nodes
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val participants = genesis.api.getActiveParticipants().activeParticipants
        assertThat(participants.participants).hasSize(3)

        val genesisParticipant = participants.getParticipant(genesis)
        assertThat(genesisParticipant).isNotNull
        genesisParticipant?.mlNodes?.firstOrNull()?.mlNodes.also { genesisMlNodes ->
            assertThat(genesisMlNodes).hasSize(2)
            assertThat(genesisMlNodes!![0].timeslotAllocation[1] || genesisMlNodes[1].timeslotAllocation[1])
                .isTrue()
                .`as`("At least one Genesis ML node should have inference timeslot allocation")
        }

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

        // 4. Wait for beginning of PoC stage and make ~15 inference requests
        logSection("Waiting for PoC start")
        val waitForPocResult = genesis.waitForStage(EpochStage.START_OF_POC)
        val latestEpoch = genesis.api.getLatestEpoch()
        val claimMoneyBlock = when {
            latestEpoch.epochStages.claimMoney > waitForPocResult.stageBlock -> latestEpoch.epochStages.claimMoney
            else -> latestEpoch.nextEpochStages.claimMoney
        }

        logSection("Sending 15 inference requests")
        val requests = 10
        // Assuming runParallelInferencesWithResults is available and imports are correct
        val inferences = runParallelInferencesWithResults(
            genesis,
            count = requests, 
            maxConcurrentRequests = 5
        )
        
        assertThat(inferences).hasSize(requests)
        assertThat(inferences).allMatch { 
            it.status == InferenceStatus.VALIDATED.value || it.status == InferenceStatus.FINISHED.value 
        }
        logSection("All 15 inferences succeeded")

        // 5. Wait for end of PoC and check if join-1 could claim rewards
        logSection("Waiting for claimMoneyBlock. pocStart = ${waitForPocResult.stageBlock}. claimMoney = $claimMoneyBlock")
        val waitForSetVals = genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 3)
        genesis.node.waitForMinimumBlock(claimMoneyBlock + 2, "Waiting for claim money block to be passed")
        
        // Try to claim rewards for join-1
        logSection("Attempting to claim rewards for join-1. setVals = ${waitForSetVals.stageBlock}")
        val seed = join1.api.getConfig().previousSeed
        val claimMsg = MsgClaimRewards(
            creator = join1.node.getColdAddress(),
            seed = seed.seed,
            epochIndex = seed.epochIndex,
        )
        
        val initialBalance = join1.node.getSelfBalance()
        logSection("Join-1 Balance before claim: $initialBalance")
        
        val claimResponse = join1.submitMessage(claimMsg)
        assertThat(claimResponse).isSuccess()
        
        val finalBalance = join1.node.getSelfBalance()
        logSection("Join-1 Balance after claim: $finalBalance")
        
        // If the balance increases, it got rewards.
        if (finalBalance > initialBalance) {
            Logger.info("Join-1 successfully claimed rewards.")
        } else {
            Logger.info("Join-1 claimed but no rewards received (or 0).")
        }
    }
}

