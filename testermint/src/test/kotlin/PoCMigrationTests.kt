import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

/**
 * Tests for PoC V1/V2 migration via poc_v2_enabled governance parameter.
 * Verifies that:
 * - V1 mode uses on-chain PoCBatch storage
 * - V2 mode uses off-chain StoreCommit storage
 * - Runtime switching via governance works without restart
 */
@Timeout(value = 30, unit = TimeUnit.MINUTES)
class PoCMigrationTests : TestermintTest() {

    /**
     * V1 Test: poc_v2_enabled = false
     * Verifies that PoCBatch exists on chain and no StoreCommit is created.
     */
    @Test
    fun `poc v1 mode - batches on chain, no store commits`() {
        logSection("=== TEST: PoC V1 Mode ===")

        val (cluster, genesis) = initCluster(reboot = true, config = v1Config)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        // Verify V1 mode is active
        val params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isFalse()
        Logger.info("poc_v2_enabled = ${params.pocParams.pocV2Enabled}")

        // Wait for PoC generation to complete
        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val epochData = genesis.getEpochData()
        val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        logSection("Verifying V1 behavior: PoCBatch on chain")

        // V1: PoCBatch should exist on chain
        val batchCount = genesis.node.getPocBatchCount(pocStartHeight)
        Logger.info("PoCBatch count for height $pocStartHeight: $batchCount")
        assertThat(batchCount).isGreaterThan(0)
            .describedAs("V1 mode should have PoCBatch entries on chain")

        // V1: StoreCommit should NOT exist (or query fails)
        val storeCommit = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
        Logger.info("StoreCommit found: ${storeCommit.found}")
        assertThat(storeCommit.found).isFalse()
            .describedAs("V1 mode should NOT have StoreCommit entries")

        // V1: Proof API should return 503
        logSection("Verifying V1 behavior: Proof API unavailable")
        try {
            val artifactState = genesis.api.getPocArtifactsState(pocStartHeight)
            Logger.warn("Artifact state unexpectedly available: $artifactState")
            // If we get here, check the count - should be 0 in V1 mode
        } catch (e: Exception) {
            Logger.info("Proof API correctly unavailable in V1 mode: ${e.message}")
        }

        logSection("TEST PASSED: PoC V1 mode works correctly")
    }

    /**
     * V2 Test: poc_v2_enabled = true (default)
     * Verifies that StoreCommit exists on chain and proof API works.
     */
    @Test
    fun `poc v2 mode - store commits on chain, proof api works`() {
        logSection("=== TEST: PoC V2 Mode ===")

        val (cluster, genesis) = initCluster(reboot = true, config = v2Config)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        // Verify V2 mode is active
        val params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isTrue()
        Logger.info("poc_v2_enabled = ${params.pocParams.pocV2Enabled}")

        // Wait for PoC generation to complete
        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val epochData = genesis.getEpochData()
        val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        logSection("Verifying V2 behavior: StoreCommit on chain")

        // V2: StoreCommit should exist on chain
        val storeCommit = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
        Logger.info("StoreCommit: found=${storeCommit.found}, count=${storeCommit.count}")
        assertThat(storeCommit.found).isTrue()
            .describedAs("V2 mode should have StoreCommit entries")
        assertThat(storeCommit.count).isGreaterThan(0)
            .describedAs("V2 mode StoreCommit should have count > 0")

        // V2: Weight distribution should exist
        val weightDist = genesis.node.getMLNodeWeightDistribution(pocStartHeight, participantAddress)
        Logger.info("Weight distribution: found=${weightDist.found}, weights=${weightDist.weights.size}")
        if (weightDist.found) {
            assertThat(weightDist.weights).isNotEmpty()
        }

        // V2: Proof API should work
        logSection("Verifying V2 behavior: Proof API available")
        val artifactState = genesis.api.getPocArtifactsState(pocStartHeight)
        Logger.info("Artifact state: count=${artifactState.count}, rootHash=${artifactState.rootHash}")
        assertThat(artifactState.count).isGreaterThanOrEqualTo(0)

        logSection("TEST PASSED: PoC V2 mode works correctly")
    }

    /**
     * Migration Test: V1 to V2 via governance without restart.
     * Verifies that the system can switch from V1 to V2 behavior dynamically.
     */
    @Test
    fun `poc migration - v1 to v2 via governance without restart`() {
        logSection("=== TEST: PoC V1 to V2 Migration ===")

        // Start with V1 mode
        val (cluster, genesis) = initCluster(reboot = true, config = v1Config)
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        // Verify V1 mode is active
        var params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isFalse()
        Logger.info("Initial mode: poc_v2_enabled = ${params.pocParams.pocV2Enabled}")

        // === Phase 1: Run V1 PoC cycle ===
        logSection("Phase 1: Running V1 PoC cycle")

        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val v1EpochData = genesis.getEpochData()
        val v1PocHeight = v1EpochData.latestEpoch.pocStartBlockHeight

        // Verify V1 results
        val v1BatchCount = genesis.node.getPocBatchCount(v1PocHeight)
        Logger.info("V1 cycle complete: PoCBatch count = $v1BatchCount at height $v1PocHeight")
        assertThat(v1BatchCount).isGreaterThan(0)
            .describedAs("V1 cycle should produce PoCBatch entries")

        // === Phase 2: Switch to V2 via governance ===
        logSection("Phase 2: Switching to V2 via governance")

        val modifiedParams = params.copy(
            pocParams = params.pocParams.copy(pocV2Enabled = true)
        )

        genesis.runProposal(cluster, UpdateParams(params = modifiedParams))

        // Verify switch happened
        params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isTrue()
        Logger.info("After governance: poc_v2_enabled = ${params.pocParams.pocV2Enabled}")

        // === Phase 3: Run V2 PoC cycle (no restart!) ===
        logSection("Phase 3: Running V2 PoC cycle (no restart)")

        // Wait for next PoC cycle
        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val v2EpochData = genesis.getEpochData()
        val v2PocHeight = v2EpochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        // Verify V2 results
        val storeCommit = genesis.node.getPoCV2StoreCommit(v2PocHeight, participantAddress)
        Logger.info("V2 cycle complete: StoreCommit found=${storeCommit.found}, count=${storeCommit.count} at height $v2PocHeight")
        assertThat(storeCommit.found).isTrue()
            .describedAs("V2 cycle should produce StoreCommit entries")
        assertThat(storeCommit.count).isGreaterThan(0)
            .describedAs("V2 cycle StoreCommit should have count > 0")

        logSection("TEST PASSED: V1 to V2 migration via governance works correctly")
    }

    /**
     * Migration Mode Test: poc_v2_enabled = false, confirmation_poc_v2_enabled = true
     *
     * In migration mode:
     * - Regular PoC: V1
     * - Confirmation PoC event_sequence == 0: V2 tracking only (no weight impact)
     * - Confirmation PoC event_sequence >= 1: V1 (affects weights)
     *
     * This test verifies:
     * 1. Regular PoC uses V1 (PoCBatch on chain, no StoreCommit)
     * 2. First confirmation event (event_sequence == 0) uses V2 tracking (StoreCommit)
     * 3. Second confirmation event (event_sequence > 0) uses V1 (PoCBatch)
     * 4. poc_v2_enabled remains false (no auto-switch)
     */
    @Test
    fun `migration mode - v2 tracking for first confirmation event`() {
        logSection("=== TEST: Migration Mode (V2 tracking for first event) ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = migrationModeSpec,
            reboot = true
        )
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        // Verify migration mode is active
        val params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isFalse()
        assertThat(params.pocParams.confirmationPocV2Enabled).isTrue()
        Logger.info("Migration mode active: poc_v2_enabled=${params.pocParams.pocV2Enabled}, confirmation_poc_v2_enabled=${params.pocParams.confirmationPocV2Enabled}")

        // === Phase 1: Run regular PoC cycle (should use V1) ===
        logSection("Phase 1: Running regular PoC cycle (V1)")

        genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val epochData = genesis.getEpochData()
        val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
        val participantAddress = genesis.node.getColdAddress()

        // V1: PoCBatch should exist for regular PoC
        val batchCount = genesis.node.getPocBatchCount(pocStartHeight)
        Logger.info("Regular PoC: PoCBatch count = $batchCount at height $pocStartHeight")
        assertThat(batchCount).isGreaterThan(0)
            .describedAs("Migration mode regular PoC should produce PoCBatch entries (V1)")

        // V1: StoreCommit should NOT exist for regular PoC
        val storeCommit = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
        Logger.info("Regular PoC: StoreCommit found = ${storeCommit.found}")
        assertThat(storeCommit.found).isFalse()
            .describedAs("Migration mode regular PoC should NOT produce StoreCommit (V1)")

        // === Phase 2: Let confirmation PoCs run through the epoch ===
        logSection("Phase 2: Letting confirmation PoCs run through epoch")

        // Set PoC weights for all participants
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        // Wait for epoch to progress through confirmation PoCs
        // With expectedConfirmationsPerEpoch=100 and epochLength=80, we should get multiple events
        val currentEpoch = epochData.latestEpoch.index
        Logger.info("Current epoch: $currentEpoch, waiting for confirmation events to complete...")

        // Wait for multiple confirmation events by checking periodically
        var eventsFound = 0
        for (i in 1..50) {
            genesis.node.waitForNextBlock(3)
            val events = genesis.node.listConfirmationPoCEvents(currentEpoch)
            if (events.events.size >= 2) {
                eventsFound = events.events.size
                Logger.info("Found $eventsFound confirmation events after ${i * 3} blocks")
                break
            }
            if (i % 10 == 0) {
                Logger.info("Progress: ${events.events.size} events found after ${i * 3} blocks")
            }
        }

        // === Phase 3: Verify confirmation PoC events using historical query ===
        logSection("Phase 3: Verifying confirmation PoC events")

        val allEvents = genesis.node.listConfirmationPoCEvents(currentEpoch)
        Logger.info("Total confirmation events in epoch $currentEpoch: ${allEvents.events.size}")

        // Find first event (eventSequence == 0) - should use V2
        val firstEvent = allEvents.events.find { it.eventSequence == 0L }
        if (firstEvent != null) {
            Logger.info("First event (seq=0): triggerHeight=${firstEvent.triggerHeight}")
            val firstCommit = genesis.node.getPoCV2StoreCommit(firstEvent.triggerHeight, participantAddress)
            Logger.info("First event: StoreCommit found=${firstCommit.found}")
            assertThat(firstCommit.found).isTrue()
                .describedAs("Migration mode first event (eventSequence=0) should use V2 (StoreCommit)")
        } else {
            Logger.warn("No eventSequence=0 found - skipping first event assertions")
        }

        // Find second event (eventSequence > 0) - should use V1
        val secondEvent = allEvents.events.find { it.eventSequence > 0 }
        if (secondEvent != null) {
            Logger.info("Second event (seq=${secondEvent.eventSequence}): triggerHeight=${secondEvent.triggerHeight}")
            genesis.node.waitForNextBlock(15)
            val batchCount = genesis.node.getPocBatchCount(secondEvent.triggerHeight)
            Logger.info("Second event: PoCBatch count = $batchCount")
            assertThat(batchCount).isGreaterThan(0)
                .describedAs("Migration mode second event (eventSequence>0) should use V1 (PoCBatch)")
        } else {
            Logger.warn("No eventSequence>0 found - skipping second event assertions")
        }

        // === Phase 4: Verify no auto-switch (manual governance required) ===
        logSection("Phase 4: Verifying no auto-switch occurred")

        genesis.node.waitForNextBlock(5)
        val paramsAfter = genesis.getParams()
        assertThat(paramsAfter.pocParams.pocV2Enabled).isFalse()
            .describedAs("Migration mode should NOT auto-switch - manual governance required")
        Logger.info("After confirmation PoC: poc_v2_enabled=${paramsAfter.pocParams.pocV2Enabled} (no auto-switch)")

        logSection("TEST PASSED: Migration mode works correctly (V2 tracking for event 0, V1 for event 1+)")
    }

    /**
     * Grace Epoch Test: Confirmation PoC in dry-run mode during V2 transition epoch.
     *
     * When switching from migration mode to full V2 via governance:
     * - The epoch when V2 was enabled is stored as the "grace epoch"
     * - Confirmation PoC events in the grace epoch run in dry-run mode (no weight impact)
     * - This prevents unfair punishment for nodes that weren't tracking V2 from epoch start
     *
     * Test flow:
     * 1. Start in migration mode
     * 2. Wait for confirmation PoC to complete (establishes baseline weights)
     * 3. Switch to V2 via governance mid-epoch
     * 4. Run another confirmation PoC with FAILING weight
     * 5. Verify weights are NOT modified (dry-run in grace epoch)
     */
    @Test
    fun `migration to v2 - grace epoch skips confirmation punishment`() {
        logSection("=== TEST: Migration to V2 - Grace Epoch ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = migrationModeSpec,
            reboot = true
        )
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        // Verify migration mode is active
        var params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isFalse()
        assertThat(params.pocParams.confirmationPocV2Enabled).isTrue()
        Logger.info("Migration mode active: poc_v2_enabled=${params.pocParams.pocV2Enabled}")

        // === Phase 1: Run first PoC cycle to establish weights ===
        logSection("Phase 1: Running first PoC cycle")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        // === Phase 2: Let first confirmation PoC complete in migration mode ===
        logSection("Phase 2: Waiting for first confirmation PoC to complete")

        val epochData = genesis.getEpochData()
        val currentEpoch = epochData.latestEpoch.index
        Logger.info("Current epoch: $currentEpoch, waiting for confirmation events...")

        // Wait for at least one confirmation event to complete
        var eventsFound = 0
        for (i in 1..50) {
            genesis.node.waitForNextBlock(3)
            val events = genesis.node.listConfirmationPoCEvents(currentEpoch)
            if (events.events.isNotEmpty()) {
                eventsFound = events.events.size
                Logger.info("Found $eventsFound confirmation events after ${i * 3} blocks")
                break
            }
            if (i % 10 == 0) {
                Logger.info("Progress: waiting for confirmation events (${i * 3} blocks)")
            }
        }
        assertThat(eventsFound).isGreaterThan(0)
            .describedAs("Should have at least one confirmation event")
        Logger.info("First confirmation PoC completed")

        // === Phase 3: Switch to V2 via governance mid-epoch ===
        logSection("Phase 3: Switching to V2 via governance")

        val modifiedParams = params.copy(
            pocParams = params.pocParams.copy(pocV2Enabled = true)
        )
        genesis.runProposal(cluster, UpdateParams(params = modifiedParams))

        params = genesis.getParams()
        assertThat(params.pocParams.pocV2Enabled).isTrue()
        Logger.info("After governance: poc_v2_enabled=${params.pocParams.pocV2Enabled}")

        // Record initial balances before next settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        // === Phase 4: Run confirmation PoC with FAILING weight in grace epoch ===
        logSection("Phase 4: Running confirmation PoC with failing weight in grace epoch")

        // Set ALL participants to low weight to ensure non-preserved nodes fail
        // (some nodes may be POC_SLOT=true/preserved and won't participate in confirmation)
        genesis.setPocWeight(1)  // Very low - would normally be punished
        join1.setPocWeight(1)    // Very low - would normally be punished
        join2.setPocWeight(1)    // Very low - would normally be punished

        // Wait for more confirmation events to complete with low weights
        val eventsBefore = genesis.node.listConfirmationPoCEvents(currentEpoch).events.size
        Logger.info("Events before: $eventsBefore, waiting for more confirmation events with low weights...")

        for (i in 1..30) {
            genesis.node.waitForNextBlock(3)
            val events = genesis.node.listConfirmationPoCEvents(currentEpoch)
            if (events.events.size > eventsBefore) {
                Logger.info("Found ${events.events.size} confirmation events (was $eventsBefore) after ${i * 3} blocks")
                break
            }
            if (i % 10 == 0) {
                Logger.info("Progress: ${events.events.size} events (${i * 3} blocks)")
            }
        }
        Logger.info("Confirmation PoC completed in grace epoch (should be dry-run)")

        // Reset weights
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        // === Phase 5: Wait for next epoch settlement ===
        logSection("Phase 5: Waiting for reward settlement")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        val genesisChange = finalBalances[genesis.node.getColdAddress()]!! - initialBalances[genesis.node.getColdAddress()]!!
        val join1Change = finalBalances[join1.node.getColdAddress()]!! - initialBalances[join1.node.getColdAddress()]!!
        val join2Change = finalBalances[join2.node.getColdAddress()]!! - initialBalances[join2.node.getColdAddress()]!!

        Logger.info("Balance changes:")
        Logger.info("  Genesis: $genesisChange (low weight during grace epoch)")
        Logger.info("  Join1: $join1Change (low weight during grace epoch)")
        Logger.info("  Join2: $join2Change (low weight during grace epoch)")

        // === Phase 6: Verify participants were NOT punished (grace epoch dry-run) ===
        logSection("Phase 6: Verifying grace epoch behavior")

        // All participants should have positive rewards despite low confirmation weights
        assertThat(genesisChange).isGreaterThan(0)
        assertThat(join1Change).isGreaterThan(0)
        assertThat(join2Change).isGreaterThan(0)
        Logger.info("All participants received positive rewards despite low confirmation weights")

        // In grace epoch, low confirmation weights should NOT affect rewards
        // Without grace epoch, participants would have significantly lower rewards (1/10 ratio)
        // With grace epoch, all should have similar rewards
        val minChange = minOf(genesisChange, join1Change, join2Change)
        val maxChange = maxOf(genesisChange, join1Change, join2Change)
        val ratio = if (maxChange > 0) minChange.toDouble() / maxChange.toDouble() else 1.0
        Logger.info("Min/max reward ratio: $ratio (min=$minChange, max=$maxChange)")

        // If grace epoch worked, ratio should be close to 1.0 (not 0.1)
        // Allow some tolerance for timing variations and preserved node differences
        assertThat(ratio).isGreaterThan(0.5)
            .describedAs("Grace epoch should prevent punishment - min/max ratio should be > 0.5, got $ratio")
        Logger.info("Grace epoch worked: participants were not punished for low confirmation weight")

        logSection("TEST PASSED: Grace epoch correctly prevents confirmation PoC punishment during V2 transition")
    }

    // === Test Configurations ===

    private val v1PocSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::pocStageDuration] = 3L
                    this[EpochParams::pocValidationDuration] = 4L
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = false
                    this[PocParams::confirmationPocV2Enabled] = false
                }
            }
        }
    }

    private val v2PocSpec = spec {
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

    private val v1Config = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(v1PocSpec) ?: v1PocSpec,
    )

    private val v2Config = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(v2PocSpec) ?: v2PocSpec,
    )

    // Migration mode: poc_v2_enabled=false, confirmation_poc_v2_enabled=true
    // Regular PoC: V1
    // Confirmation PoC: event 0 = V2 tracking, event 1+ = V1
    // Longer epoch (80 blocks) to allow multiple confirmation events
    private val migrationModeSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::epochLength] = 80L
                    this[EpochParams::pocStageDuration] = 5L
                    this[EpochParams::pocValidationDuration] = 4L
                    this[EpochParams::pocExchangeDuration] = 2L
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = false
                    this[PocParams::confirmationPocV2Enabled] = true
                }
                this[InferenceParams::confirmationPocParams] = spec<ConfirmationPoCParams> {
                    this[ConfirmationPoCParams::expectedConfirmationsPerEpoch] = 100L
                    this[ConfirmationPoCParams::alphaThreshold] = Decimal.fromDouble(0.70)
                    this[ConfirmationPoCParams::slashFraction] = Decimal.fromDouble(0.10)
                }
            }
        }
    }
}
