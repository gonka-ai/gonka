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

        val regularAnchor = genesis.api.getLatestEpoch().latestEpoch.pocStartBlockHeight
        val preservedSnapshot = genesis.node.queryPreservedNodesSnapshot(regularAnchor)
        assertThat(preservedSnapshot.found).isTrue()
        val modelId = extractSingleModelId(genesis.api.getNodes())
        val preservedNodeIds = preservedNodeIdsForModel(preservedSnapshot, modelId)
        // Two-node cluster with the default pocSlotAllocation preserves exactly one node
        // per PoC episode. Pinning the cardinality guards against the sampler silently
        // returning an empty set or preserving everything.
        assertThat(preservedNodeIds).hasSize(1)

        val allocatedNode = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (_, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                }
            }
            nodes.single { node -> node.node.id in preservedNodeIds }
        }

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                if (node.node.id == allocatedNode.node.id) {
                    assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                    assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
                } else {
                    assertThat(node.state.currentStatus).isEqualTo("POC")
                    assertThat(node.state.intendedStatus).isEqualTo("POC")
                }
            }
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        checkParticipantWeights(genesis.node, genesisParticipantKey)

        // After the next epoch boundary, a fresh regular-PoC snapshot exists at the new
        // PocStartBlockHeight. Verifying it is non-empty restores the "allocation
        // actually happened" guarantee the old TimeslotAllocation[1] proxy gave.
        val nextRegularAnchor = genesis.api.getLatestEpoch().latestEpoch.pocStartBlockHeight
        val nextPreservedSnapshot = genesis.node.queryPreservedNodesSnapshot(nextRegularAnchor)
        assertThat(nextPreservedSnapshot.found).isTrue()
        val nextPreservedNodeIds = preservedNodeIdsForModel(nextPreservedSnapshot, modelId)
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
