import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.assertj.core.data.Percentage
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class ConfirmationPoCMultiNodeTests : TestermintTest() {
    
    data class NodeAllocation(val nodeId: String, val preserved: Boolean, val weight: Long)

    // 16m
    @Test
    fun `confirmation PoC with multiple MLNodes - capped rewards with POC_SLOT allocation`() {
        logSection("=== TEST: Confirmation PoC with Multiple MLNodes - POC_SLOT Allocation ===")
        
        // Initialize cluster with custom spec for confirmation PoC testing
        val confirmationSpec = createConfirmationPoCSpec(expectedConfirmationsPerEpoch = 100, pocSlotAllocation = 0.05)
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = confirmationSpec,
            reboot = true,
            resetMlNodes = false  // Don't reset - we want to keep our 3-node configuration
        )
        logSection("Setting up mock weights to avoid power capping")
        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]
        // Set genesis nodes to weight=10 per node (total 30), join nodes to weight=50 to avoid power capping Genesis
        // Genesis: 30/130 = 23% < 30% (no capping)
        // Note: Each node generates its own nonces, so setting to 10 means each of genesis's 3 nodes generates 10, totaling 30
        genesis.addNodes(2)
        genesis.setPocWeight(10)
        join1.setPocWeight(50)
        join2.setPocWeight(50)

        genesis.waitForNextEpoch()

        logSection("✅ Cluster Initialized Successfully with genesis having 3 MLNodes!")
        

        logSection("Verifying genesis has 3 mock server containers")
        // The additional mock servers should have been started by initCluster with reboot=true
        var genesisNodes = genesis.api.getNodes()
        Logger.info("Genesis has ${genesisNodes.size} nodes registered")
        genesisNodes.forEach { node ->
            Logger.info("  Node: ${node.node.id} at ${node.node.host}:${node.node.pocPort}")
        }

        logSection("Waiting for second PoC cycle to establish confirmation_weight=50 for join nodes")
        // The confirmation_weight is initialized from the previous epoch's weight during epoch formation
        // We need a second cycle so join nodes' confirmation_weight gets set to 50
        genesis.waitForNextEpoch()

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        val confirmedWeightPerNode = 8L
        
        logSection("Setting PoC mocks for confirmation")
        // During confirmation PoC, each POC_SLOT=false node will return weight=8 (reduced from 10)
        Logger.info("  Genesis: each node returns weight=$confirmedWeightPerNode (reduced from 10)")
        Logger.info("  Join1: weight=50 per node (full confirmation)")
        Logger.info("  Join2: weight=50 per node (full confirmation)")
        genesis.setPocWeight(confirmedWeightPerNode)
        join1.setPocWeight(50)
        join2.setPocWeight(50)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

        logSection("Querying preserved snapshot for Genesis's 3 nodes")
        genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)

        val preservedSnapshot = waitForPreservedSnapshot(genesis, confirmationEvent.triggerHeight)
        val modelId = extractSingleModelId(genesisNodes)
        val preservedNodeIds = preservedNodeIdsForModel(preservedSnapshot, modelId)
        val preservedAllocation = waitForNodeAllocations(genesis, preservedNodeIds, expectedCount = 3)
        logSection("Genesis MLNode preserved allocation:")
        preservedAllocation.forEach {
            Logger.info("  Node ${it.nodeId}: preserved=${it.preserved}, weight=${it.weight}")
        }

        val numPreserved = preservedAllocation.count { it.preserved }
        val numParticipating = preservedAllocation.count { !it.preserved }

        require(numParticipating > 0) {
            "All ${preservedAllocation.size} nodes were preserved, leaving no nodes for confirmation validation."
        }

        genesisNodes.forEach { nodeResponse ->
            if (nodeResponse.node.id in preservedNodeIds) {
                assertThat(nodeResponse.state.currentStatus).isEqualTo("INFERENCE")
            } else {
                assertThat(nodeResponse.state.currentStatus).isEqualTo("POC")
            }
        }

        val expectedFinalWeight = (numPreserved * 10) + (numParticipating * confirmedWeightPerNode)

        Logger.info("Genesis weight breakdown:")
        Logger.info("  Preserved nodes: $numPreserved × 10 = ${numPreserved * 10}")
        Logger.info("  Participating nodes: $numParticipating × $confirmedWeightPerNode = ${numParticipating * confirmedWeightPerNode}")
        Logger.info("  Expected final weight: $expectedFinalWeight")
        
        logSection("Waiting for confirmation PoC validation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        Logger.info("Confirmation PoC validation phase active")
        
        logSection("Waiting for confirmation PoC completion")
        waitForConfirmationPoCCompletion(genesis)
        Logger.info("Confirmation PoC completed (event cleared)")
        
        // Reset mocks to full weight after confirmation
        genesis.setPocWeight(10)
        join1.setPocWeight(50)
        join2.setPocWeight(50)

        logSection("Waiting for NEXT epoch where confirmation weights will be applied")
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("New epoch started, confirmation weights will be used in settlement")
        
        // Record balances AFTER confirmation but BEFORE settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )
        
        logSection("Waiting for reward settlement with confirmation weights")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        
        logSection("Verifying rewards are capped for Genesis based on POC_SLOT allocation")
        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )
        
        val genesisChange = finalBalances[genesis.node.getColdAddress()]!! - initialBalances[genesis.node.getColdAddress()]!!
        val join1Change = finalBalances[join1.node.getColdAddress()]!! - initialBalances[join1.node.getColdAddress()]!!
        val join2Change = finalBalances[join2.node.getColdAddress()]!! - initialBalances[join2.node.getColdAddress()]!!
        
        Logger.info("Balance changes:")
        Logger.info("  Genesis: $genesisChange (preserved: ${numPreserved}×10=${numPreserved * 10}, participating: ${numParticipating}×8=${numParticipating * confirmedWeightPerNode}, final=$expectedFinalWeight)")
        Logger.info("  Join1: $join1Change (weight=50)")
        Logger.info("  Join2: $join2Change (weight=50)")
        
        // All participants should have positive rewards
        assertThat(genesisChange).isGreaterThan(0)
        assertThat(join1Change).isGreaterThan(0)
        assertThat(join2Change).isGreaterThan(0)
        Logger.info("  All participants received positive rewards")
        
        // Join1 and Join2 should have identical rewards (both weight=50, will be capped)
        logSection("Verifying Join1 and Join2 receive identical rewards")
        assertThat(join1Change).isCloseTo(join2Change, Offset.offset(5L))
        Logger.info("  Join1 and Join2 received identical rewards: $join1Change")
        
        // Genesis should have rewards proportional to expectedFinalWeight
        logSection("Verifying Genesis rewards match expected ratio based on POC_SLOT allocation")
        val genesisRatio = genesisChange.toDouble() / join1Change.toDouble()
        // Calculate expected ratio accounting for power capping at settlement
        // After confirmation: Genesis=26, Join1=50, Join2=50, Total=126
        val expectedRatio = expectedFinalWeight.toDouble() / 50
        assertThat(genesisRatio).isCloseTo(expectedRatio, Offset.offset(0.1))
        Logger.info("  Genesis reward ratio: $genesisRatio (expected: $expectedRatio)")
        Logger.info("  Ratio verification: ${genesisChange}/${join1Change}")
        
        logSection("TEST PASSED: Confirmation PoC correctly handles multiple MLNodes with POC_SLOT allocation")
        Logger.info("  Test validated with $numPreserved preserved nodes and $numParticipating participating nodes")
        Logger.info("  Final weight: $expectedFinalWeight = (${numPreserved}×10) + (${numParticipating}×8)")
    }

    // 12 m
    @Test
    fun `confirmation PoC with multiple MLNodes - capped rewards with POC_SLOT allocation 2`() {
        logSection("=== TEST: Confirmation PoC with Multiple MLNodes - POC_SLOT Allocation ===")

        // Initialize cluster with custom spec for confirmation PoC testing
        val confirmationSpec = createConfirmationPoCSpec(
            expectedConfirmationsPerEpoch = 100,
            alphaThreshold = 0.toDouble()
        )
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = confirmationSpec,
            reboot = true,
            resetMlNodes = false  // Don't reset - we want to keep our 3-node configuration
        )
        logSection("Adding two nodes for genesis and setting power for all nodes")
        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]
        genesis.addNodes(2)
        genesis.setPocWeight(101)
        join1.setPocWeight(200)
        join2.setPocWeight(250)
        genesis.waitForNextEpoch()

        var genesisNodes = genesis.api.getNodes()
        Logger.info("Genesis has ${genesisNodes.size} nodes registered")
        genesisNodes.forEach { node ->
            Logger.info("  Node: ${node.node.id} at ${node.node.host}:${node.node.pocPort}")
        }

        logSection("Waiting for second PoC cycle to establish confirmation_weight=50 for join nodes")
        // The confirmation_weight is initialized from the previous epoch's weight during epoch formation
        // We need a second cycle so join nodes' confirmation_weight gets set to 50
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        val confirmedWeightPerNode = 51L

        logSection("Setting PoC mocks for confirmation")
        Logger.info("  Genesis: each node returns weight=$confirmedWeightPerNode (reduced from 30)")
        Logger.info("  Join1: weight=200 per node (full confirmation)")
        Logger.info("  Join2: weight=250 per node (full confirmation)")
        genesis.setPocWeight(confirmedWeightPerNode)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

        logSection("Querying preserved snapshot for Genesis's 3 nodes")
        genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)

        val preservedSnapshot = waitForPreservedSnapshot(genesis, confirmationEvent.triggerHeight)
        val modelId = extractSingleModelId(genesisNodes)
        val preservedNodeIds = preservedNodeIdsForModel(preservedSnapshot, modelId)
        val preservedAllocation = waitForNodeAllocations(genesis, preservedNodeIds, expectedCount = 3)

        logSection("Genesis MLNode preserved allocation:")
        preservedAllocation.forEach {
            Logger.info("  Node ${it.nodeId}: preserved=${it.preserved}, weight=${it.weight}")
        }

        val numPreserved = preservedAllocation.count { it.preserved }
        val numParticipating = preservedAllocation.count { !it.preserved }

        require(numParticipating > 0) {
            "All ${preservedAllocation.size} nodes were preserved, leaving no nodes for confirmation validation."
        }

        val expectedFinalWeight = (numPreserved * 101) + (numParticipating * confirmedWeightPerNode)

        Logger.info("Genesis weight breakdown:")
        Logger.info("  Preserved nodes: $numPreserved × 101 = ${numPreserved * 101}")
        Logger.info("  Participating nodes: $numParticipating × $confirmedWeightPerNode = ${numParticipating * confirmedWeightPerNode}")
        Logger.info("  Expected final weight: $expectedFinalWeight")

        logSection("Waiting for confirmation PoC validation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        Logger.info("Confirmation PoC validation phase active")

        logSection("Waiting for confirmation PoC completion")
        waitForConfirmationPoCCompletion(genesis)
        Logger.info("Confirmation PoC completed (event cleared)")

        // Reset mocks to full weight after confirmation
        genesis.setPocWeight(101)

        logSection("Waiting for NEXT epoch where confirmation weights will be applied")
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("New epoch started, confirmation weights will be used in settlement")

        // Record balances AFTER confirmation but BEFORE settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        logSection("Waiting for reward settlement with confirmation weights")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        logSection("Verifying rewards are capped for Genesis based on POC_SLOT allocation")
        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        val genesisChange = finalBalances[genesis.node.getColdAddress()]!! - initialBalances[genesis.node.getColdAddress()]!!
        val join1Change = finalBalances[join1.node.getColdAddress()]!! - initialBalances[join1.node.getColdAddress()]!!
        val join2Change = finalBalances[join2.node.getColdAddress()]!! - initialBalances[join2.node.getColdAddress()]!!

        Logger.info("Balance changes:")
        Logger.info("  Genesis: $genesisChange")
        Logger.info("  Join1: $join1Change")
        Logger.info("  Join2: $join2Change")

        // All participants should have positive rewards
        assertThat(genesisChange).isGreaterThan(0)
        assertThat(join1Change).isGreaterThan(0)
        assertThat(join2Change).isGreaterThan(0)
        Logger.info("  All participants received positive rewards")

        val totalChange = (genesisChange + join1Change + join2Change).toDouble()
        val genesisRatio = genesisChange / totalChange
        val join1Ratio = join1Change / totalChange
        val join2Ratio = join2Change / totalChange

        val totalExpectedWeight = expectedFinalWeight + 200.0 + 250.0
        assertThat(genesisRatio).isCloseTo(expectedFinalWeight / totalExpectedWeight, Percentage.withPercentage(1.5))
        assertThat(join1Ratio).isCloseTo(200.0 / totalExpectedWeight, Percentage.withPercentage(1.5))
        assertThat(join2Ratio).isCloseTo(250.0 / totalExpectedWeight, Percentage.withPercentage(1.5))
    }

}

// Helper functions

fun createConfirmationPoCSpec(
    expectedConfirmationsPerEpoch: Long,
    alphaThreshold: Double = 0.70,
    pocSlotAllocation: Double = 0.33  // Default to 33% to ensure some nodes remain POC_SLOT=false
): Spec<AppState> {
    // Configure epoch params and confirmation PoC params
    // epochLength=40 provides sufficient inference phase window for confirmation PoC trigger
    // pocStageDuration=5, pocValidationDuration=4 gives confirmation PoC enough time to complete
    // pocSlotAllocation controls what fraction of nodes get POC_SLOT=true (serve inference during PoC)
    // Setting lower values (e.g., 0.33) ensures nodes remain POC_SLOT=false for confirmation validation
    return spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::epochLength] = 40L
                    this[EpochParams::pocStageDuration] = 5L
                    this[EpochParams::pocValidationDuration] = 4L
                    this[EpochParams::pocExchangeDuration] = 2L
                    this[EpochParams::pocSlotAllocation] = Decimal.fromDouble(pocSlotAllocation)
                    this[EpochParams::confirmationPocSafetyWindow] = 0L
                }
                this[InferenceParams::confirmationPocParams] = spec<ConfirmationPoCParams> {
                    this[ConfirmationPoCParams::expectedConfirmationsPerEpoch] = expectedConfirmationsPerEpoch
                    this[ConfirmationPoCParams::alphaThreshold] = Decimal.fromDouble(alphaThreshold)
                    this[ConfirmationPoCParams::slashFraction] = Decimal.fromDouble(0.10)
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocDataPruningEpochThreshold] = 10L
                }
            }
        }
    }
}

fun waitForConfirmationPoCTrigger(pair: LocalInferencePair, maxBlocks: Int = 100): ConfirmationPoCEvent? {
    var attempts = 0
    while (attempts < maxBlocks) {
        val epochData = pair.getEpochData()
        if (epochData.isConfirmationPocActive && epochData.activeConfirmationPocEvent != null) {
            return epochData.activeConfirmationPocEvent
        }
        pair.node.waitForNextBlock(2)
        attempts++
    }
    return null
}

fun waitForConfirmationPoCPhase(
    pair: LocalInferencePair,
    targetPhase: ConfirmationPoCPhase,
    maxBlocks: Int = 100
) {
    var attempts = 0
    var connectionRetry = 0
    while (attempts < maxBlocks && connectionRetry < 5) {
        val epochData =
            try {
                pair.getEpochData()
            } catch (e: Exception) {
                Logger.error("Error getting epoch data", e)
                connectionRetry += 1
                Thread.sleep(connectionRetry * 100L)
                continue
            }
        connectionRetry = 0  // Reset on successful call
        if (epochData.isConfirmationPocActive &&
            epochData.activeConfirmationPocEvent?.phase == targetPhase) {
            return
        }
        pair.node.waitForNextBlock(2)
        attempts++
    }
    error("Timeout waiting for confirmation PoC phase: $targetPhase")
}

fun waitForPreservedSnapshot(
    pair: LocalInferencePair,
    anchorHeight: Long,
    maxBlocks: Int = 20
): PreservedNodesSnapshotQueryResponse {
    repeat(maxBlocks) {
        val snapshot = pair.node.queryPreservedNodesSnapshot(anchorHeight)
        if (snapshot.found) {
            return snapshot
        }
        pair.node.waitForNextBlock(1)
    }
    error("Timeout waiting for preserved snapshot at anchor $anchorHeight")
}

fun preservedNodeIdsForModel(
    snapshot: PreservedNodesSnapshotQueryResponse,
    modelId: String
): Set<String> {
    return snapshot.snapshot
        ?.modelPreservedNodes
        ?.firstOrNull { it.modelId == modelId }
        ?.preservedNodeIds
        ?.toSet()
        ?: emptySet()
}

fun extractSingleModelId(nodes: List<NodeResponse>): String {
    return nodes.asSequence()
        .mapNotNull { nodeResponse -> nodeResponse.state.epochMlNodes?.keys?.singleOrNull() }
        .firstOrNull()
        ?: error("Could not determine single model id from node epoch data")
}

fun waitForNodeAllocations(
    pair: LocalInferencePair,
    preservedNodeIds: Set<String>,
    expectedCount: Int,
    maxBlocks: Int = 20
): List<ConfirmationPoCMultiNodeTests.NodeAllocation> {
    repeat(maxBlocks) {
        val allocations = pair.api.getNodes().mapNotNull { nodeResponse ->
            val epochMlNodes = nodeResponse.state.epochMlNodes
            if (epochMlNodes.isNullOrEmpty()) {
                null
            } else {
                val (_, mlNodeInfo) = epochMlNodes.entries.first()
                ConfirmationPoCMultiNodeTests.NodeAllocation(
                    nodeId = nodeResponse.node.id,
                    preserved = nodeResponse.node.id in preservedNodeIds,
                    weight = mlNodeInfo.pocWeight.toLong()
                )
            }
        }
        if (allocations.size == expectedCount) {
            return allocations
        }
        pair.node.waitForNextBlock(1)
    }
    error("Timeout waiting for $expectedCount node allocations")
}

fun waitForConfirmationPoCCompletion(
    pair: LocalInferencePair,
    maxBlocks: Int = 100
) {
    var attempts = 0
    while (attempts < maxBlocks) {
        val epochData = pair.getEpochData()
        if (!epochData.isConfirmationPocActive) {
            return
        }
        pair.node.waitForNextBlock(2)
        attempts++
    }
    error("Timeout waiting for confirmation PoC completion")
}
