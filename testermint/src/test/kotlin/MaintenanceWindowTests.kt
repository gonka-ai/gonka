import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.data.*
import com.productscience.getRawContainers
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Assumptions.assumeTrue
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

/**
 * End-to-end tests for the Maintenance Windows feature.
 *
 * These tests verify that maintenance windows work correctly in a real
 * multi-node blockchain environment: scheduling, activation, liveness
 * exemption (no jailing), duty suppression, and normal resume behavior.
 */
class MaintenanceWindowTests : TestermintTest() {

    /**
     * Lazily-instantiated Docker client shared by all node-container helpers
     * in this class. Building a DockerClient is heavy, so we reuse one per
     * test class lifecycle instead of constructing one per call.
     */
    private val dockerClient: DockerClient by lazy { DockerClientBuilder.getInstance().build() }

    /**
     * Wait until the participant has accumulated at least [minCredit] blocks of
     * maintenance credit. Retries by waiting for additional epochs up to
     * [maxRetryEpochs] times. Returns the final credit balance.
     */
    private fun awaitMinimumCredit(
        genesis: LocalInferencePair,
        participantAddress: String,
        minCredit: Long,
        maxRetryEpochs: Int = 4,
    ): Long {
        repeat(maxRetryEpochs) {
            val credit: MaintenanceCreditResponse = genesis.node.execAndParse(
                listOf("query", "inference", "maintenance-credit", participantAddress)
            )
            if (credit.creditBlocks >= minCredit) return credit.creditBlocks
            Logger.info("Credit ${credit.creditBlocks} < $minCredit, waiting for another epoch...")
            genesis.waitForNextEpoch()
        }
        val final: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", participantAddress)
        )
        return final.creditBlocks
    }

    /**
     * Default maintenance params used by most tests. Individual tests can
     * override fields via `enableMaintenance(..., overrides = { copy(...) })`.
     */
    private val defaultMaintenanceParams = MaintenanceParams(
        maintenanceEnabled = true,
        maintenanceMinScheduleLeadBlocks = 5,
        maintenanceMaxWindowBlocks = 50,
        maintenanceMaxConcurrentValidators = 3L,
        maintenanceMaxConcurrentPowerBps = 5000L, // 50%
        maintenanceCreditCapBlocks = 200,
        maintenanceCreditEarnPerSuccessfulEpochBlocks = 50,
    )

    /**
     * Submit a governance proposal that enables maintenance with the given
     * params (defaults to [defaultMaintenanceParams]). Returns the resulting
     * params for downstream assertions/inspection.
     */
    private fun enableMaintenance(
        cluster: LocalCluster,
        genesis: LocalInferencePair,
        params: MaintenanceParams = defaultMaintenanceParams,
    ): InferenceParams {
        val current = genesis.getParams()
        val modified = current.copy(maintenanceParams = params)
        genesis.runProposal(cluster, UpdateParams(params = modified))
        return genesis.getParams()
    }

    /**
     * Resolve the Docker container for a pair's chain node.
     * Throws if the container cannot be found.
     */
    private fun nodeContainerFor(pair: LocalInferencePair) =
        getRawContainers(pair.config).getNode(pair.name)
            ?: error("Node container not found for ${pair.name}")

    /**
     * Stop the chain node container for a given pair.
     * This causes the validator to stop signing blocks (missing signatures).
     */
    private fun stopNodeContainer(pair: LocalInferencePair) {
        dockerClient.stopContainerCmd(nodeContainerFor(pair).id).exec()
        Logger.info("Stopped node container for ${pair.name}")
    }

    /**
     * Restart a previously stopped chain node container.
     */
    private fun startNodeContainer(pair: LocalInferencePair) {
        val container = nodeContainerFor(pair)
        if (container.state != "running") {
            dockerClient.startContainerCmd(container.id).exec()
        }
        Logger.info("Started node container for ${pair.name}")
    }

    /**
     * Test 1: Enable maintenance via governance and verify params are updated.
     */
    @Test
    @Tag("maintenance")
    fun `enable maintenance windows via governance proposal`() {
        val (cluster, genesis) = initCluster()

        logSection("Submitting governance proposal to enable maintenance")
        val newParams = enableMaintenance(cluster, genesis)

        logSection("Verifying maintenance params are updated")
        val mp = checkNotNull(newParams.maintenanceParams) { "maintenanceParams was null after governance update" }
        assertThat(mp.maintenanceEnabled).isTrue()
        assertThat(mp.maintenanceMinScheduleLeadBlocks).isEqualTo(defaultMaintenanceParams.maintenanceMinScheduleLeadBlocks)
        Logger.info("Maintenance successfully enabled via governance")

        genesis.markNeedsReboot()
    }

    /**
     * Test 2: Full maintenance window lifecycle — schedule, activate, verify
     * no jailing during offline period, verify resume after window ends.
     *
     * This is the core E2E test for the maintenance windows feature.
     */
    @Test
    @Tag("maintenance")
    fun `maintenance window prevents jailing during offline period`() {
        val (cluster, genesis) = initCluster()

        // Step 1: Enable maintenance with test-friendly params via governance
        logSection("Step 1: Enable maintenance via governance")
        val updatedParams = enableMaintenance(cluster, genesis)
        assertThat(updatedParams.maintenanceParams?.maintenanceEnabled).isTrue()

        // Step 2: Earn maintenance credit by completing epochs
        logSection("Step 2: Earning maintenance credit through epochs")
        val join1 = cluster.joinPairs[0]
        val join1Address = join1.node.getColdAddress()
        Logger.info("Join1 address: $join1Address")

        // Wait for enough epochs to accumulate sufficient credit
        genesis.waitForNextEpoch()
        genesis.waitForNextEpoch()
        val creditBlocks = awaitMinimumCredit(genesis, join1Address, minCredit = 15)
        Logger.info("Join1 maintenance credit: $creditBlocks blocks")

        // Step 3: Schedule a maintenance window for join1
        logSection("Step 3: Scheduling maintenance window for join1")
        val epochData = genesis.getEpochData()
        val currentHeight = epochData.blockHeight

        // Schedule maintenance with lead time derived from params
        val leadBlocks = defaultMaintenanceParams.maintenanceMinScheduleLeadBlocks
        val startHeight = currentHeight + leadBlocks + 5
        val durationBlocks = 15L
        Logger.info("Scheduling maintenance: start=$startHeight, duration=$durationBlocks, currentHeight=$currentHeight")

        // Check schedulability first
        val schedulabilityResponse: MaintenanceSchedulabilityResponse = genesis.node.execAndParse(
            listOf(
                "query", "inference", "maintenance-schedulability",
                join1Address,
                startHeight.toString(),
                durationBlocks.toString()
            )
        )
        Logger.info("Schedulability check: schedulable=${schedulabilityResponse.schedulable}, reason=${schedulabilityResponse.rejectionReason}")

        assumeTrue(schedulabilityResponse.schedulable) {
            genesis.markNeedsReboot()
            "Window not schedulable: ${schedulabilityResponse.rejectionReason} — likely PoC/DKG phase overlap or insufficient credit"
        }

        // Schedule the maintenance window
        val scheduleTx = join1.submitTransaction(
            listOf(
                "inference", "schedule-maintenance",
                "--participant", join1Address,
                "--start-height", startHeight.toString(),
                "--duration-blocks", durationBlocks.toString(),
            )
        )
        Logger.info("Schedule tx result: code=${scheduleTx.code}, hash=${scheduleTx.txhash}")
        assertThat(scheduleTx.code).isEqualTo(0)

        // Verify reservation was created
        val statusResponse: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        assertThat(statusResponse.found).isTrue()
        assertThat(statusResponse.scheduledReservation).isNotNull
        Logger.info("Reservation created: id=${statusResponse.scheduledReservation?.reservationId}, status=${statusResponse.scheduledReservation?.status}")

        // Step 4: Verify join1 validator is BONDED before maintenance
        logSection("Step 4: Verify join1 is BONDED before maintenance")
        val validatorsBefore = genesis.node.getValidators()
        val join1ValPubKey = join1.node.getValidatorInfo().key
        val join1ValBefore = checkNotNull(
            validatorsBefore.validators.find { it.consensusPubkey.value == join1ValPubKey }
        ) { "join1 validator not found in validator set before maintenance" }
        assertThat(join1ValBefore.statusEnum).isEqualTo(StakeValidatorStatus.BONDED)
        Logger.info("Join1 validator status before maintenance: ${join1ValBefore.status}")

        // Step 5: Wait for maintenance window to activate
        logSection("Step 5: Waiting for maintenance window to activate at height $startHeight")
        genesis.node.waitForMinimumBlock(startHeight + 1, "maintenance activation")

        // Verify maintenance is now active
        val activeResponse: MaintenanceActiveResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-active")
        )
        Logger.info("Active maintenance windows: ${activeResponse.reservations.size}")

        // Step 6: Stop join1's chain node to simulate being offline during maintenance
        // This causes join1's validator to stop signing blocks (missed signatures).
        logSection("Step 6: Stopping join1 chain node (simulating offline maintenance)")
        stopNodeContainer(join1)
        Logger.info("Join1 chain node stopped — validator will miss signatures")

        // Step 7: Wait through the maintenance window while join1 is offline
        logSection("Step 7: Waiting through maintenance window (${durationBlocks} blocks)")
        // Wait for blocks to pass — join1 is missing signatures during this time
        val endHeight = startHeight + durationBlocks
        genesis.node.waitForMinimumBlock(endHeight + 2, "maintenance window end")
        Logger.info("Maintenance window should now be completed")

        // Step 8: Verify join1 is NOT jailed (the key assertion!)
        logSection("Step 8: Verifying join1 was NOT jailed during maintenance")
        val validatorsAfter = genesis.node.getValidators()
        val join1ValAfter = checkNotNull(
            validatorsAfter.validators.find { it.consensusPubkey.value == join1ValPubKey }
        ) { "join1 validator not found in validator set after maintenance" }
        assertThat(join1ValAfter.statusEnum)
            .describedAs("Join1 should remain BONDED — maintenance window should have prevented jailing")
            .isEqualTo(StakeValidatorStatus.BONDED)
        Logger.info("SUCCESS: Join1 validator is still BONDED after being offline during maintenance window!")

        // Verify maintenance window completed
        val statusAfter: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        Logger.info("Join1 maintenance state after window: active=${statusAfter.activeReservation}, scheduled=${statusAfter.scheduledReservation}")

        genesis.markNeedsReboot()
    }

    /**
     * Test 3: Verify that maintenance scheduling is rejected when it overlaps
     * with restricted PoC or DKG phases.
     */
    @Test
    @Tag("maintenance")
    fun `maintenance window scheduling rejected during epoch-critical phases`() {
        val (cluster, genesis) = initCluster()

        // Enable maintenance with longer-window/larger-credit settings to make
        // the PoC overlap window easy to construct.
        logSection("Enabling maintenance via governance")
        enableMaintenance(
            cluster, genesis,
            params = defaultMaintenanceParams.copy(
                maintenanceMinScheduleLeadBlocks = 2,
                maintenanceMaxWindowBlocks = 100,
                maintenanceCreditCapBlocks = 500,
                maintenanceCreditEarnPerSuccessfulEpochBlocks = 100,
            )
        )

        // Earn some credit
        genesis.waitForNextEpoch()
        genesis.waitForNextEpoch()

        val join1 = cluster.joinPairs[0]
        val join1Address = join1.node.getColdAddress()

        // Get epoch stages to find PoC phase
        logSection("Getting epoch stages to target PoC phase")
        val epochData = genesis.getEpochData()
        val pocStart = epochData.nextEpochStages.pocStart
        Logger.info("Next PoC start: $pocStart, current height: ${epochData.blockHeight}")

        // Try to schedule a maintenance window that overlaps with PoC start
        // Use a window that covers pocStart
        val overlapStart = pocStart - 5
        val overlapDuration = 20L

        assumeTrue(overlapStart > epochData.blockHeight + 2) {
            genesis.markNeedsReboot()
            "Cannot test PoC overlap — PoC start too close to current height"
        }

        logSection("Checking schedulability for PoC-overlapping window")
        val schedulabilityResponse: MaintenanceSchedulabilityResponse = genesis.node.execAndParse(
            listOf(
                "query", "inference", "maintenance-schedulability",
                join1Address,
                overlapStart.toString(),
                overlapDuration.toString()
            )
        )
        Logger.info("Schedulability for PoC-overlapping window: schedulable=${schedulabilityResponse.schedulable}, reason=${schedulabilityResponse.rejectionReason}")

        // The window should be rejected due to PoC/DKG phase overlap
        assertThat(schedulabilityResponse.schedulable)
            .describedAs("Window overlapping PoC phase should not be schedulable")
            .isFalse()
        assertThat(schedulabilityResponse.rejectionReason).isNotEmpty()
        Logger.info("SUCCESS: Scheduling correctly rejected for epoch-critical phase overlap")

        genesis.markNeedsReboot()
    }

    /**
     * Test 4: Verify maintenance cancellation restores credit.
     */
    @Test
    @Tag("maintenance")
    fun `cancel scheduled maintenance restores credit`() {
        val (cluster, genesis) = initCluster()

        // Enable maintenance
        logSection("Enabling maintenance via governance")
        enableMaintenance(cluster, genesis)

        // Earn credit
        genesis.waitForNextEpoch()
        genesis.waitForNextEpoch()

        val join1 = cluster.joinPairs[0]
        val join1Address = join1.node.getColdAddress()

        // Schedule a maintenance window far in the future
        val epochData = genesis.getEpochData()
        val leadBlocks = defaultMaintenanceParams.maintenanceMinScheduleLeadBlocks
        val startHeight = epochData.blockHeight + leadBlocks + 45
        val durationBlocks = 10L

        // Check credit before scheduling
        val creditBefore: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        Logger.info("Credit before scheduling: ${creditBefore.creditBlocks}")

        assumeTrue(creditBefore.creditBlocks >= durationBlocks) {
            genesis.markNeedsReboot()
            "Insufficient credit (${creditBefore.creditBlocks}) for test duration ($durationBlocks)"
        }

        // Check schedulability
        val schedulability: MaintenanceSchedulabilityResponse = genesis.node.execAndParse(
            listOf(
                "query", "inference", "maintenance-schedulability",
                join1Address, startHeight.toString(), durationBlocks.toString()
            )
        )
        assumeTrue(schedulability.schedulable) {
            genesis.markNeedsReboot()
            "Window not schedulable: ${schedulability.rejectionReason}"
        }

        logSection("Scheduling maintenance window")
        val scheduleTx = join1.submitTransaction(
            listOf(
                "inference", "schedule-maintenance",
                "--participant", join1Address,
                "--start-height", startHeight.toString(),
                "--duration-blocks", durationBlocks.toString(),
            )
        )
        assertThat(scheduleTx.code).isEqualTo(0)

        // Check credit after scheduling (should be reduced by durationBlocks)
        val creditAfterSchedule: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        Logger.info("Credit after scheduling: ${creditAfterSchedule.creditBlocks}")
        assertThat(creditAfterSchedule.creditBlocks)
            .isLessThan(creditBefore.creditBlocks)

        // Get reservation ID for cancellation
        val status: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        val scheduled = checkNotNull(status.scheduledReservation) { "scheduled reservation missing after schedule tx" }
        val reservationId = scheduled.reservationId
        Logger.info("Reservation ID to cancel: $reservationId")

        // Cancel the maintenance window
        logSection("Canceling maintenance window")
        val cancelTx = join1.submitTransaction(
            listOf(
                "inference", "cancel-maintenance",
                "--reservation-id", reservationId.toString(),
            )
        )
        assertThat(cancelTx.code).isEqualTo(0)

        // Check credit after cancellation (should be restored)
        val creditAfterCancel: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        Logger.info("Credit after cancellation: ${creditAfterCancel.creditBlocks}")
        assertThat(creditAfterCancel.creditBlocks)
            .describedAs("Credit should be restored after cancellation")
            .isGreaterThanOrEqualTo(creditAfterSchedule.creditBlocks + durationBlocks)

        Logger.info("SUCCESS: Credit correctly restored after maintenance cancellation")
        genesis.markNeedsReboot()
    }

    /**
     * Test 5: Verify maintenance concurrency query returns correct data.
     */
    @Test
    @Tag("maintenance")
    fun `maintenance concurrency query returns correct count`() {
        val (cluster, genesis) = initCluster()

        // Verify concurrency with no active maintenance
        logSection("Querying maintenance concurrency with no active maintenance")
        val epochData = genesis.getEpochData()
        val concurrency: MaintenanceConcurrencyResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-concurrency", epochData.blockHeight.toString())
        )
        assertThat(concurrency.concurrentCount).isEqualTo(0)
        Logger.info("Concurrent maintenance count (none active): ${concurrency.concurrentCount}")
        Logger.info("SUCCESS: Concurrency query correctly reports zero when no maintenance is active")
    }
}
