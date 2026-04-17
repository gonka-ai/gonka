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
            resetMlNodes = false
        )
        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        // Weights: genesis 3 nodes × 10 = 30, joins 1 node each at 50. 30/130 < 30% => no power cap.
        genesis.addNodes(2)
        genesis.setPocWeight(10)
        join1.setPocWeight(50)
        join2.setPocWeight(50)
        genesis.waitForNextEpoch()

        val genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)
        genesisNodes.forEach { node ->
            Logger.info("  Node: ${node.node.id} at ${node.node.host}:${node.node.pocPort}")
        }

        val honestGenesisWeight = 10L
        val dishonestGenesisWeight = 8L

        runConfirmationPoCScenario(
            genesis = genesis,
            participants = listOf(genesis, join1, join2),
            genesisNodeIds = genesisNodes.map { it.node.id }.toSet(),
            modelId = extractSingleModelId(genesisNodes),
            honestGenesisWeight = honestGenesisWeight,
            dishonestGenesisWeight = dishonestGenesisWeight,
            assertRewards = { changes, expectedFinalWeight ->
                val genesisChange = changes.getValue(genesis.node.getColdAddress())
                val join1Change = changes.getValue(join1.node.getColdAddress())
                val join2Change = changes.getValue(join2.node.getColdAddress())

                assertThat(genesisChange).isGreaterThan(0)
                assertThat(join1Change).isGreaterThan(0)
                assertThat(join2Change).isGreaterThan(0)
                // Both joins have identical weight -> identical reward (modulo rounding).
                assertThat(join1Change).isCloseTo(join2Change, Offset.offset(5L))

                val genesisRatio = genesisChange.toDouble() / join1Change.toDouble()
                val expectedRatio = expectedFinalWeight.toDouble() / 50
                assertThat(genesisRatio).isCloseTo(expectedRatio, Offset.offset(0.1))
                Logger.info("Genesis reward ratio: $genesisRatio (expected: $expectedRatio)")
            }
        )
    }

    // 12 m
    @Test
    fun `confirmation PoC with multiple MLNodes - capped rewards with POC_SLOT allocation 2`() {
        logSection("=== TEST: Confirmation PoC with Multiple MLNodes - POC_SLOT Allocation ===")

        val confirmationSpec = createConfirmationPoCSpec(
            expectedConfirmationsPerEpoch = 100,
            alphaThreshold = 0.toDouble()
        )
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = confirmationSpec,
            reboot = true,
            resetMlNodes = false
        )
        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]
        genesis.addNodes(2)
        genesis.setPocWeight(101)
        join1.setPocWeight(200)
        join2.setPocWeight(200)
        genesis.waitForNextEpoch()

        val genesisNodes = genesis.api.getNodes()
        assertThat(genesisNodes).hasSize(3)
        genesisNodes.forEach { node ->
            Logger.info("  Node: ${node.node.id} at ${node.node.host}:${node.node.pocPort}")
        }

        val honestGenesisWeight = 101L
        val dishonestGenesisWeight = 51L

        runConfirmationPoCScenario(
            genesis = genesis,
            participants = listOf(genesis, join1, join2),
            genesisNodeIds = genesisNodes.map { it.node.id }.toSet(),
            modelId = extractSingleModelId(genesisNodes),
            honestGenesisWeight = honestGenesisWeight,
            dishonestGenesisWeight = dishonestGenesisWeight,
            assertRewards = { changes, expectedFinalWeight ->
                val genesisChange = changes.getValue(genesis.node.getColdAddress())
                val join1Change = changes.getValue(join1.node.getColdAddress())
                val join2Change = changes.getValue(join2.node.getColdAddress())

                assertThat(genesisChange).isGreaterThan(0)
                assertThat(join1Change).isGreaterThan(0)
                assertThat(join2Change).isGreaterThan(0)

                val totalChange = (genesisChange + join1Change + join2Change).toDouble()
                val genesisRatio = genesisChange / totalChange
                val join1Ratio = join1Change / totalChange
                val join2Ratio = join2Change / totalChange

                // Joins are symmetric (both 200) so no single participant exceeds the
                // 40% per-participant power cap in CalculateParticipantBitcoinRewards.
                // With genesis folded to 153 via CPoC, shares are 27.7 / 36.2 / 36.2 --
                // pure-proportional reward distribution holds within tolerance.
                val totalExpectedWeight = expectedFinalWeight + 200.0 + 200.0
                assertThat(genesisRatio).isCloseTo(expectedFinalWeight / totalExpectedWeight, Percentage.withPercentage(1.5))
                assertThat(join1Ratio).isCloseTo(200.0 / totalExpectedWeight, Percentage.withPercentage(1.5))
                assertThat(join2Ratio).isCloseTo(200.0 / totalExpectedWeight, Percentage.withPercentage(1.5))
            }
        )
    }

}

// Helper functions

fun createConfirmationPoCSpec(
    expectedConfirmationsPerEpoch: Long,
    alphaThreshold: Double = 0.70,
    pocSlotAllocation: Double = 0.33,  // 33% preservation target
): Spec<AppState> {
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

// runConfirmationPoCScenario drives a single test epoch end-to-end under the full-reading
// semantic: regular PoC runs with honest weights, every CPoC event in the epoch measures
// the dishonest genesis weight, and settlement rewards reflect min over all event readings.
//
// The caller supplies the honest/dishonest weights and a reward-assertion callback. This
// helper collects every CPoC event from the chain, fetches each event's preserved snapshot,
// simulates the full-reading ConfirmationWeight, cross-checks the simulation against the
// chain-stored value, waits for settlement, and hands balance changes to the callback.
fun runConfirmationPoCScenario(
    genesis: LocalInferencePair,
    participants: List<LocalInferencePair>,
    genesisNodeIds: Set<String>,
    modelId: String,
    honestGenesisWeight: Long,
    dishonestGenesisWeight: Long,
    assertRewards: (changes: Map<String, Long>, expectedFinalWeight: Long) -> Unit,
) {
    logSection("Starting test epoch (honest regular PoC; CPoC events will measure dishonest weight)")
    genesis.waitForStage(EpochStage.START_OF_POC)
    val testEpoch = genesis.api.getLatestEpoch().latestEpoch.index
    Logger.info("Test epoch: $testEpoch")

    // Wait until regular PoC commits are frozen. Regular-PoC-mining results under honest
    // weights; switching mocks now only affects CPoC events in the inference phase.
    genesis.waitForStage(EpochStage.END_OF_POC_VALIDATION)
    genesis.setPocWeight(dishonestGenesisWeight)
    Logger.info("Genesis mock set to dishonest weight=$dishonestGenesisWeight for epoch $testEpoch")

    // Let the test epoch fully play out. All CPoC events for $testEpoch are finalized on
    // chain by the time the next epoch's PoC starts.
    genesis.waitForStage(EpochStage.START_OF_POC)
    genesis.setPocWeight(honestGenesisWeight)
    Logger.info("Genesis mock restored to honest weight=$honestGenesisWeight for next epoch")

    // Simulate ConfirmationWeight from every CPoC event's snapshot.
    val events = genesis.node.listConfirmationPoCEvents(testEpoch).events
    require(events.isNotEmpty()) { "No CPoC events fired in epoch $testEpoch" }
    Logger.info("Collected ${events.size} CPoC events for epoch $testEpoch")

    val expectedFinalWeight = simulateConfirmationWeight(
        pair = genesis,
        events = events,
        participantNodeIds = genesisNodeIds,
        modelId = modelId,
        initialPerNodeWeight = honestGenesisWeight,
        eventPerNodeWeight = dishonestGenesisWeight,
    )
    Logger.info("Simulated expectedFinalWeight=$expectedFinalWeight over ${events.size} events")

    // Cross-check simulation against chain-stored ConfirmationWeight.
    val genesisAddr = genesis.node.getColdAddress()
    val storedFinalWeight = genesis.node
        .queryEpochGroupData(testEpoch, modelId = "")
        .epochGroupData
        .validationWeights
        .first { it.memberAddress == genesisAddr }
        .confirmationWeight
    assertThat(storedFinalWeight)
        .describedAs("simulation should match chain-stored ConfirmationWeight for genesis")
        .isEqualTo(expectedFinalWeight)

    // Record balances before settlement, wait for distribution, compare.
    val addresses = participants.map { it.node.getColdAddress() }
    val initialBalances = addresses.associateWith { addr ->
        participants.first { it.node.getColdAddress() == addr }.node.getSelfBalance()
    }

    logSection("Waiting for reward settlement with confirmation weights")
    genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

    val finalBalances = addresses.associateWith { addr ->
        participants.first { it.node.getColdAddress() == addr }.node.getSelfBalance()
    }
    val changes = addresses.associateWith { addr ->
        finalBalances.getValue(addr) - initialBalances.getValue(addr)
    }
    changes.forEach { (addr, delta) -> Logger.info("  $addr: change=$delta") }

    assertRewards(changes, expectedFinalWeight)
}

// simulateConfirmationWeight applies the chain's full-reading model to a single participant
// across all events of a single epoch and returns the min of:
//   - initialReading = |participantNodeIds| * initialPerNodeWeight
//   - per-event reading = preservedNodes * initialPerNodeWeight + participatingNodes * eventPerNodeWeight
// Events that have no snapshot on chain (e.g. never reached GENERATION) are skipped.
fun simulateConfirmationWeight(
    pair: LocalInferencePair,
    events: List<ConfirmationPoCEvent>,
    participantNodeIds: Set<String>,
    modelId: String,
    initialPerNodeWeight: Long,
    eventPerNodeWeight: Long,
): Long {
    val initialReading = participantNodeIds.size * initialPerNodeWeight
    var minReading = initialReading

    for (event in events) {
        val snap = pair.node.queryPreservedNodesSnapshot(event.triggerHeight)
        if (!snap.found) {
            Logger.info("  event seq=${event.eventSequence} trigger=${event.triggerHeight}: no snapshot, skipping")
            continue
        }
        val preservedForParticipant = preservedNodeIdsForModel(snap, modelId).intersect(participantNodeIds)
        val numPreserved = preservedForParticipant.size
        val numParticipating = participantNodeIds.size - numPreserved
        val reading = numPreserved * initialPerNodeWeight + numParticipating * eventPerNodeWeight
        Logger.info(
            "  event seq=${event.eventSequence} trigger=${event.triggerHeight}: " +
                "preserved=$numPreserved participating=$numParticipating reading=$reading"
        )
        if (reading < minReading) {
            minReading = reading
        }
    }
    return minReading
}
