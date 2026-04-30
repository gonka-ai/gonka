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
            assertRewards = { changes, expectedFinalWeight, _ ->
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
            assertRewards = { changes, expectedFinalWeight, cappedWeights ->
                val genesisAddr = genesis.node.getColdAddress()
                val genesisChange = changes.getValue(genesisAddr)
                val join1Change = changes.getValue(join1.node.getColdAddress())
                val join2Change = changes.getValue(join2.node.getColdAddress())

                assertThat(genesisChange).isGreaterThan(0)
                assertThat(join1Change).isGreaterThan(0)
                assertThat(join2Change).isGreaterThan(0)

                val totalChange = (genesisChange + join1Change + join2Change).toDouble()
                val genesisRatio = genesisChange / totalChange
                val join1Ratio = join1Change / totalChange
                val join2Ratio = join2Change / totalChange

                // Genesis raw consensus = 101 * 3 = 303, then voting-power cap reduces
                // it to ~266 (Weight). CalculateParticipantBitcoinRewards rescales
                // ConfirmationWeight to the post-cap scale:
                //   effective = ConfirmationWeight * Weight / rawTotal
                //              = expectedFinalWeight * cappedGenesis / 303
                // Joins (200 each) are uncapped (Weight == rawTotal), so their share
                // is unchanged. Reward distribution is then proportional to effective.
                val genesisRawTotal = honestGenesisWeight * 3
                val cappedGenesis = cappedWeights.getValue(genesisAddr)
                val effectiveGenesis = if (cappedGenesis < genesisRawTotal)
                    expectedFinalWeight * cappedGenesis / genesisRawTotal
                else
                    expectedFinalWeight
                val totalExpectedWeight = effectiveGenesis + 200.0 + 200.0
                assertThat(genesisRatio).isCloseTo(effectiveGenesis / totalExpectedWeight, Percentage.withPercentage(1.5))
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
    modelId: String,
    participantId: String,
): Set<String> {
    return snapshot.snapshot
        ?.modelPreservedNodes
        ?.firstOrNull { it.modelId == modelId }
        ?.participants
        ?.firstOrNull { it.participantId == participantId }
        ?.nodeIds
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
// helper live-captures each CPoC event's preserved snapshot as it enters GENERATION
// (single-slot chain storage means we must observe, not fetch by trigger height later),
// simulates the full-reading ConfirmationWeight, cross-checks the simulation against the
// chain-stored value, waits for settlement, and hands balance changes to the callback.
fun runConfirmationPoCScenario(
    genesis: LocalInferencePair,
    participants: List<LocalInferencePair>,
    genesisNodeIds: Set<String>,
    modelId: String,
    honestGenesisWeight: Long,
    dishonestGenesisWeight: Long,
    assertRewards: (
        changes: Map<String, Long>,
        expectedFinalWeight: Long,
        cappedWeights: Map<String, Long>,
    ) -> Unit,
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

    val capturedReadings = captureConfirmationPoCReadings(
        pair = genesis,
        testEpoch = testEpoch,
        participantNodeIds = genesisNodeIds,
        modelId = modelId,
        initialPerNodeWeight = honestGenesisWeight,
        eventPerNodeWeight = dishonestGenesisWeight,
    )
    genesis.setPocWeight(honestGenesisWeight)
    Logger.info("Genesis mock restored to honest weight=$honestGenesisWeight for next epoch")

    require(capturedReadings.isNotEmpty()) { "No CPoC events captured in epoch $testEpoch" }
    Logger.info("Captured ${capturedReadings.size} CPoC events for epoch $testEpoch")

    val initialReading = genesisNodeIds.size * honestGenesisWeight
    val expectedFinalWeight = capturedReadings.fold(initialReading) { acc, r -> minOf(acc, r) }
    Logger.info("Simulated expectedFinalWeight=$expectedFinalWeight over ${capturedReadings.size} events")

    // Cross-check simulation against chain-stored ConfirmationWeight, and capture
    // post-cap consensus Weight per participant for the rescaling assertion below.
    val genesisAddr = genesis.node.getColdAddress()
    val vws = genesis.node
        .queryEpochGroupData(testEpoch, modelId = "")
        .epochGroupData
        .validationWeights
    val storedFinalWeight = vws.first { it.memberAddress == genesisAddr }.confirmationWeight
    val cappedWeights = vws.associate { it.memberAddress to it.weight }
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

    assertRewards(changes, expectedFinalWeight, cappedWeights)
}

// captureConfirmationPoCReadings runs until the test epoch ends, capturing one reading
// per CPoC event the first block it is observed in GENERATION or later. Chain storage
// is single-slot, so the snapshot must be read while the event is still current. Returns
// per-event readings: preserved*initialPerNodeWeight + participating*eventPerNodeWeight.
fun captureConfirmationPoCReadings(
    pair: LocalInferencePair,
    testEpoch: Long,
    participantNodeIds: Set<String>,
    modelId: String,
    initialPerNodeWeight: Long,
    eventPerNodeWeight: Long,
): List<Long> {
    val readings = mutableListOf<Long>()
    val capturedTriggers = mutableSetOf<Long>()
    val participantAddr = pair.node.getColdAddress()
    while (true) {
        val latest = pair.api.getLatestEpoch().latestEpoch.index
        if (latest != testEpoch) break

        val epochData = try {
            pair.getEpochData()
        } catch (e: Exception) {
            Logger.error("captureConfirmationPoCReadings: error fetching epoch data", e)
            pair.node.waitForNextBlock(1)
            continue
        }

        val ev = epochData.activeConfirmationPocEvent
        if (ev != null &&
            ev.phase.value >= ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION.value &&
            ev.triggerHeight !in capturedTriggers
        ) {
            val snap = pair.node.queryPreservedNodesSnapshot()
            if (!snap.found) {
                Logger.info("  event seq=${ev.eventSequence} trigger=${ev.triggerHeight}: no snapshot yet")
            } else {
                capturedTriggers.add(ev.triggerHeight)
                val preservedForParticipant = preservedNodeIdsForModel(snap, modelId, participantAddr).intersect(participantNodeIds)
                val numPreserved = preservedForParticipant.size
                val numParticipating = participantNodeIds.size - numPreserved
                val reading = numPreserved * initialPerNodeWeight + numParticipating * eventPerNodeWeight
                readings.add(reading)
                Logger.info(
                    "  event seq=${ev.eventSequence} trigger=${ev.triggerHeight}: " +
                        "preserved=$numPreserved participating=$numParticipating reading=$reading"
                )
            }
        }
        pair.node.waitForNextBlock(1)
    }
    return readings
}
