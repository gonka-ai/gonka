import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Disabled
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class NodeAdminStateTests : TestermintTest() {

    @Test
    fun `test node disable during inference phase`() {
        val (_, genesis) = initCluster(reboot = true)
        genesis.waitForNextInferenceWindow()

        val genesisValidatorBeforeDisabled = genesis.node.getStakeValidator()
        assertThat(genesisValidatorBeforeDisabled.tokens).isEqualTo(10)
        assertThat(genesisValidatorBeforeDisabled.status).isEqualTo(StakeValidatorStatus.BONDED.value)

        logSection("Getting initial nodes")
        val nodes = genesis.api.getNodes()
        assertThat(nodes).isNotEmpty()
        
        val nodeToDisable = nodes.first()
        val nodeId = nodeToDisable.node.id
        Logger.info("Testing with node: $nodeId")
        
        // Verify node is initially enabled
        assertThat(nodeToDisable.state.adminState?.enabled ?: true)
            .isTrue()
            .`as`("Node should be enabled initially")
        
        logSection("Disabling node during inference phase")
        val disableResponse = genesis.api.disableNode(nodeId)
        assertThat(disableResponse.nodeId).isEqualTo(nodeId)
        assertThat(disableResponse.message).contains("disabled successfully")
        
        // Verify node state after disable
        val nodesAfterDisable = genesis.api.getNodes()
        val disabledNode = nodesAfterDisable.first { it.node.id == nodeId }
        assertThat(disabledNode.state.adminState?.enabled)
            .isFalse()
            .`as`("Node should be disabled")
        
        val disableEpoch = disabledNode.state.adminState?.epoch ?: 0UL
        Logger.info("Node disabled at epoch: $disableEpoch")
        
        logSection("Making inference request to verify disabled node still serves")
        val inferenceResult = getInferenceResult(genesis)
        assertThat(inferenceResult).isNotNull
        
        logSection("Waiting for PoC phase to verify node stops")
        genesis.waitForStage(EpochStage.START_OF_POC)
        
        // Give reconciliation some time to kick in
        genesis.node.waitForNextBlock(2)
        
        // At this point, the disabled node should not participate in PoC
        // We can verify this by checking node states or attempting operations
        
        logSection("Re-enabling node")
        val enableResponse = genesis.api.enableNode(nodeId)
        assertThat(enableResponse.nodeId).isEqualTo(nodeId)
        assertThat(enableResponse.message).contains("enabled successfully")
        
        // Verify node is enabled
        val nodesAfterEnable = genesis.api.getNodes()
        val enabledNode = nodesAfterEnable.first { it.node.id == nodeId }
        assertThat(enabledNode.state.adminState?.enabled)
            .isTrue()
            .`as`("Node should be enabled again")

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 3)
        val genesisValidatorAfterNodeIsDisabled = genesis.node.getStakeValidator()
        assertThat(genesisValidatorAfterNodeIsDisabled.tokens).isEqualTo(0)
        assertThat(genesisValidatorAfterNodeIsDisabled.status).isEqualTo(StakeValidatorStatus.UNBONDING.value)
    }

    @Test
    fun `test node disable during PoC phase`() {
        val (_, genesis) = initCluster(reboot = true)
        
        logSection("Waiting for PoC phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        
        logSection("Getting nodes during PoC")
        val nodes = genesis.api.getNodes()
        val nodeToDisable = nodes.first()
        val nodeId = nodeToDisable.node.id
        
        logSection("Disabling node during PoC phase")
        val disableResponse = genesis.api.disableNode(nodeId)
        assertThat(disableResponse.nodeId).isEqualTo(nodeId)
        
        val nodesAfterDisable = genesis.api.getNodes()
        val disabledNode = nodesAfterDisable.first { it.node.id == nodeId }
        assertThat(disabledNode.state.adminState?.enabled)
            .isFalse()
            .`as`("Node should be disabled")

        logSection("Waiting for next epoch to verify node doesn't participate")
        genesis.waitForStage(EpochStage.END_OF_POC_VALIDATION, offset = 3)

        // It's too late to disable at PoC, so we expect the node to participate and keep its weight
        val genesisStakeValidatorWhenDisabledAtPoc = genesis.node.getStakeValidator()
        assertThat(genesisStakeValidatorWhenDisabledAtPoc.tokens).isEqualTo(10)
        assertThat(genesisStakeValidatorWhenDisabledAtPoc.status).isEqualTo(StakeValidatorStatus.BONDED.value)

        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.END_OF_POC_VALIDATION, offset = 3)

        // At this point, disabled node should not be participating in new PoC
        val genesisValidatorAfterOneMoreEpoch = genesis.node.getStakeValidator()
        assertThat(genesisValidatorAfterOneMoreEpoch.tokens).isEqualTo(0)
        assertThat(genesisValidatorAfterOneMoreEpoch.status).isEqualTo(StakeValidatorStatus.UNBONDING.value)
        
        logSection("Verifying disabled node state persists across epochs")
        val nodesInNewEpoch = genesis.api.getNodes()
        val stillDisabledNode = nodesInNewEpoch.first { it.node.id == nodeId }
        assertThat(stillDisabledNode.state.adminState?.enabled)
            .isFalse()
            .`as`("Node should remain disabled in new epoch")
    }

    @Disabled // This test doesn't make sense at the moment, rework it
    @Test
    fun `test node enable during PoC phase`() {
        val (_, genesis) = initCluster()
        
        logSection("Disabling a node first")
        val nodes = genesis.api.getNodes()
        val nodeId = nodes.first().node.id
        genesis.api.disableNode(nodeId)
        
        logSection("Waiting for PoC phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        
        logSection("Enabling node during PoC phase")
        val enableResponse = genesis.api.enableNode(nodeId)
        assertThat(enableResponse.nodeId).isEqualTo(nodeId)
        
        val nodesAfterEnable = genesis.api.getNodes()
        val enabledNode = nodesAfterEnable.first { it.node.id == nodeId }
        assertThat(enabledNode.state.adminState?.enabled)
            .isTrue()
            .`as`("Node should be enabled")
        
        val enableEpoch = enabledNode.state.adminState?.epoch ?: 0UL
        Logger.info("Node enabled at epoch: $enableEpoch during PoC phase")
        
        logSection("Waiting for inference phase to verify node serves")
        genesis.waitForStage(EpochStage.END_OF_POC_VALIDATION)
        
        // Give some time for reconciliation
        genesis.node.waitForNextBlock(2)
        
        // Node should now be able to serve inference requests
        val inferenceResult = getInferenceResult(genesis)
        assertThat(inferenceResult).isNotNull
            .`as`("Enabled node should serve inference requests")
    }

    @Disabled // Wait until we've integrated multiple nodes
    @Test
    fun `test multiple node state changes`() {
        val (cluster, genesis) = initCluster()
        
        logSection("Getting all nodes")
        val nodes = genesis.api.getNodes()
        assertThat(nodes).hasSizeGreaterThanOrEqualTo(2)
            .`as`("Need at least 2 nodes for this test")
        
        val node1Id = nodes[0].node.id
        val node2Id = nodes[1].node.id
        
        logSection("Disabling multiple nodes")
        genesis.api.disableNode(node1Id)
        genesis.api.disableNode(node2Id)
        
        val nodesAfterDisable = genesis.api.getNodes()
        val disabledNodes = nodesAfterDisable.filter { 
            it.node.id in listOf(node1Id, node2Id) 
        }
        
        disabledNodes.forEach { node ->
            assertThat(node.state.adminState?.enabled)
                .isFalse()
                .`as`("Node ${node.node.id} should be disabled")
        }
        
        logSection("Selectively re-enabling one node")
        genesis.api.enableNode(node1Id)
        
        val nodesAfterPartialEnable = genesis.api.getNodes()
        val node1 = nodesAfterPartialEnable.first { it.node.id == node1Id }
        val node2 = nodesAfterPartialEnable.first { it.node.id == node2Id }
        
        assertThat(node1.state.adminState?.enabled)
            .isTrue()
            .`as`("Node 1 should be enabled")
        assertThat(node2.state.adminState?.enabled)
            .isFalse()
            .`as`("Node 2 should remain disabled")
    }
} 