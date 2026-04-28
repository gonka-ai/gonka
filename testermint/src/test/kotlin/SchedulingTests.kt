import com.productscience.ApplicationCLI
import com.productscience.EpochStage
import com.productscience.GENESIS_KEY_NAME
import com.productscience.data.NodeResponse
import com.productscience.data.Pubkey2
import com.productscience.inferenceConfig
import com.productscience.initCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import kotlin.test.Test

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class SchedulingTests : TestermintTest() {
    @Test
    fun basicSchedulingTest() {
        val (cluster, genesis) = initCluster(reboot = true, resetMlNodes = false)
        genesis.addNodes(1)
        genesis.waitForNextEpoch()
        val genesisParticipantKey = genesis.node.getValidatorInfo()

        // Wait for all participants to join and validators to be applied
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        checkParticipantWeights(genesis.node, genesisParticipantKey) // Should have all participants by now

        genesis.waitForStage(EpochStage.START_OF_POC)

        val preservedSnapshot = genesis.node.queryPreservedNodesSnapshot()
        assertThat(preservedSnapshot.found).isTrue()
        val modelId = extractSingleModelId(genesis.api.getNodes())
        val genesisAddr = genesis.node.getColdAddress()
        val preservedNodeIds = preservedNodeIdsForModel(preservedSnapshot, modelId, genesisAddr)
        // The snapshot is chain-wide; with default pocSlotAllocation=0.5 and a cluster
        // total weight of 40 (4 nodes x weight 10), we expect at least one preserved node.
        // A non-empty set guards against the sampler silently returning nothing.
        assertThat(preservedNodeIds).isNotEmpty

        // Each of genesis's own ML nodes is either in the preserved set (INFERENCE) or not (POC).
        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (_, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                }
                val expected = if (node.node.id in preservedNodeIds) "INFERENCE" else "POC"
                assertThat(node.state.currentStatus).isEqualTo(expected)
                assertThat(node.state.intendedStatus).isEqualTo(expected)
            }
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        checkParticipantWeights(genesis.node, genesisParticipantKey)

        // After the next epoch boundary, a fresh regular-PoC snapshot has overwritten the
        // single slot. Verifying it is non-empty restores the "allocation actually happened"
        // guarantee the old TimeslotAllocation[1] proxy gave.
        val nextPreservedSnapshot = genesis.node.queryPreservedNodesSnapshot()
        assertThat(nextPreservedSnapshot.found).isTrue()
        val nextPreservedNodeIds = preservedNodeIdsForModel(nextPreservedSnapshot, modelId, genesisAddr)
        assertThat(nextPreservedNodeIds).isNotEmpty

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (_, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                }
            }
            nodes.forEach { node ->
                assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
            }
        }
    }
}

fun checkParticipantWeights(
    appCli: ApplicationCLI,
    genesisParticipantKey: Pubkey2,
    expectedGenesisTokens: Long? = null
) {
    val validators = appCli.getValidators().validators
    val participantCount = validators.size
    
    // Determine expected genesis tokens based on participant count if not specified
    val expectedTokens = expectedGenesisTokens ?: when (participantCount) {
        2 -> 10L // 2 participants: 50% cap results in 10 tokens
        3 -> 13L // 3 participants: 40% cap results in 13 tokens  
        else -> throw AssertionError("Unexpected participant count: $participantCount")
    }
    
    validators.forEach { v ->
        when (v.consensusPubkey.value) {
            genesisParticipantKey.key -> assertThat(v.tokens).isEqualTo(expectedTokens)
            else -> assertThat(v.tokens).isEqualTo(10)
        }
    }
}
