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
    
    private data class NodeAllocation(val nodeId: String, val pocSlot: Boolean, val weight: Long)

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

        logSection("Querying POC_SLOT allocation for Genesis's 3 nodes")
        genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)
        
        val pocSlotAllocation = genesisNodes.mapNotNull { nodeResponse ->
            val epochMlNodes = nodeResponse.state.epochMlNodes
            if (!epochMlNodes.isNullOrEmpty()) {
                val (_, mlNodeInfo) = epochMlNodes.entries.first()
                val timeslotAllocation = mlNodeInfo.timeslotAllocation
                val pocSlot = timeslotAllocation.getOrNull(1) ?: false  // Index 1 is POC_SLOT
                NodeAllocation(nodeResponse.node.id, pocSlot, mlNodeInfo.pocWeight.toLong())
            } else {
                null
            }
        }
        
        assertThat(pocSlotAllocation).hasSize(3)
        
        logSection("Genesis MLNode POC_SLOT allocation:")
        pocSlotAllocation.forEach { 
            Logger.info("  Node ${it.nodeId}: POC_SLOT=${it.pocSlot}, weight=${it.weight}")
        }
        
        val numPocSlotTrue = pocSlotAllocation.count { it.pocSlot }
        val numPocSlotFalse = pocSlotAllocation.count { !it.pocSlot }
        
        // Ensure we have nodes with POC_SLOT=false for confirmation validation
        require(numPocSlotFalse > 0) {
            "All ${pocSlotAllocation.size} nodes were allocated POC_SLOT=true, leaving no nodes for confirmation validation. " +
            "This test requires some nodes to remain POC_SLOT=false. Try lowering pocSlotAllocation parameter."
        }

        val confirmedWeightPerNode = 8L
        val expectedFinalWeight = (numPocSlotTrue * 10) + (numPocSlotFalse * confirmedWeightPerNode)
        
        Logger.info("Genesis weight breakdown:")
        Logger.info("  POC_SLOT=true nodes: $numPocSlotTrue × 10 = ${numPocSlotTrue * 10}")
        Logger.info("  POC_SLOT=false nodes: $numPocSlotFalse × $confirmedWeightPerNode = ${numPocSlotFalse * confirmedWeightPerNode}")
        Logger.info("  Expected final weight: $expectedFinalWeight")
        
        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")
        
        logSection("Setting PoC mocks for confirmation")
        // During confirmation PoC, each POC_SLOT=false node will return weight=8 (reduced from 10)
        Logger.info("  Genesis: each node returns weight=$confirmedWeightPerNode (reduced from 10)")
        Logger.info("    - Only $numPocSlotFalse POC_SLOT=false nodes will participate in confirmation")
        Logger.info("    - Total confirmed weight: ${numPocSlotFalse * confirmedWeightPerNode}")
        Logger.info("  Join1: weight=50 per node (full confirmation)")
        Logger.info("  Join2: weight=50 per node (full confirmation)")
        genesis.setPocWeight(confirmedWeightPerNode)
        join1.setPocWeight(50)
        join2.setPocWeight(50)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")
        
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
        Logger.info("  Genesis: $genesisChange (POC_SLOT=true: ${numPocSlotTrue}×10=${numPocSlotTrue * 10}, POC_SLOT=false: ${numPocSlotFalse}×8=${numPocSlotFalse * confirmedWeightPerNode}, final=$expectedFinalWeight)")
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
        Logger.info("  Test validated with $numPocSlotTrue POC_SLOT=true nodes and $numPocSlotFalse POC_SLOT=false nodes")
        Logger.info("  Final weight: $expectedFinalWeight = (${numPocSlotTrue}×10) + (${numPocSlotFalse}×8)")
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

        logSection("Querying POC_SLOT allocation for Genesis's 3 nodes")
        genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)

        val pocSlotAllocation = genesisNodes.mapNotNull { nodeResponse ->
            val epochMlNodes = nodeResponse.state.epochMlNodes
            if (epochMlNodes != null && epochMlNodes.isNotEmpty()) {
                val (_, mlNodeInfo) = epochMlNodes.entries.first()
                val timeslotAllocation = mlNodeInfo.timeslotAllocation
                val pocSlot = timeslotAllocation.getOrNull(1) ?: false  // Index 1 is POC_SLOT
                NodeAllocation(nodeResponse.node.id, pocSlot, mlNodeInfo.pocWeight.toLong())
            } else {
                null
            }
        }

        assertThat(pocSlotAllocation).hasSize(3)

        logSection("Genesis MLNode POC_SLOT allocation:")
        pocSlotAllocation.forEach {
            Logger.info("  Node ${it.nodeId}: POC_SLOT=${it.pocSlot}, weight=${it.weight}")
        }

        val numPocSlotTrue = pocSlotAllocation.count { it.pocSlot }
        val numPocSlotFalse = pocSlotAllocation.count { !it.pocSlot }

        // Ensure we have nodes with POC_SLOT=false for confirmation validation
        require(numPocSlotFalse > 0) {
            "All ${pocSlotAllocation.size} nodes were allocated POC_SLOT=true, leaving no nodes for confirmation validation. " +
            "This test requires some nodes to remain POC_SLOT=false. Try lowering pocSlotAllocation parameter."
        }

        val expectedFinalWeight = 203L
        val confirmedWeightPerNode = (expectedFinalWeight - 101*numPocSlotTrue) / numPocSlotFalse

        Logger.info("Genesis weight breakdown:")
        Logger.info("  POC_SLOT=true nodes: $numPocSlotTrue × 101 = ${numPocSlotTrue * 101}")
        Logger.info("  POC_SLOT=false nodes: $numPocSlotFalse × $confirmedWeightPerNode = ${numPocSlotFalse * confirmedWeightPerNode}")
        Logger.info("  Expected final weight: $expectedFinalWeight")

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        logSection("Setting PoC mocks for confirmation")
        Logger.info("  Genesis: each node returns weight=$confirmedWeightPerNode (reduced from 30)")
        Logger.info("    - Only $numPocSlotFalse POC_SLOT=false nodes will participate in confirmation")
        Logger.info("    - Total confirmed weight: ${numPocSlotFalse * confirmedWeightPerNode}")
        Logger.info("  Join1: weight=200 per node (full confirmation)")
        Logger.info("  Join2: weight=250 per node (full confirmation)")
        genesis.setPocWeight(confirmedWeightPerNode)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

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

        assertThat(genesisRatio).isCloseTo(0.3108728943338438, Percentage.withPercentage(1.0))
        assertThat(join1Ratio).isCloseTo(0.30627871362940273, Percentage.withPercentage(1.0))
        assertThat(join2Ratio).isCloseTo(0.38284839203675347, Percentage.withPercentage(1.0))
    }

    private fun getConfirmationWeights(pair: LocalInferencePair): Map<String, Pair<Long, Long>> {
        // Query active participants to get both regular weight and confirmation_weight
        val activeParticipants = pair.api.getActiveParticipants()
        
        val weights = mutableMapOf<String, Pair<Long, Long>>()
        activeParticipants.activeParticipants.participants.forEach { participant ->
            // Regular weight is the sum of poc_weight across all ml_nodes
            val regularWeight = participant.mlNodes.flatMap { it.mlNodes }.sumOf { it.pocWeight }
            
            // For confirmation weight, we need to query the epoch group data
            // For now, we'll use the regular weight as a placeholder
            // In a real implementation, this would query the ValidationWeight.confirmation_weight field
            val confirmationWeight = regularWeight  // TODO: Query actual confirmation_weight from chain
            
            weights[participant.index] = Pair(regularWeight, confirmationWeight)
        }
        
        return weights
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
