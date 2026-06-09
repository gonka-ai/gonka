import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.LocalInferencePair
import com.productscience.data.*
import com.productscience.getRawContainers
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.EpochStage
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

/**
 * End-to-end tests for the Maintenance Windows feature.
 *
 * These tests verify the two highest-signal maintenance behaviors in a real
 * multi-node blockchain environment:
 * 1. scheduling semantics (rejection, scheduling, cancellation, credit)
 * 2. lifecycle behavior (activation and offline exemption without jailing)
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

    private fun getMaintenanceParams(genesis: LocalInferencePair): MaintenanceParams {
        return checkNotNull(genesis.getParams().maintenanceParams) {
            "maintenanceParams was null in Testermint chain params"
        }
    }

    private fun waitForCometValidatorPowers(
        genesis: LocalInferencePair,
        expectedVotingPowerByPubKey: Map<String, String>,
        maxBlocks: Int = 6,
    ) {
        repeat(maxBlocks) { attempt ->
            val cometValidators = genesis.node.getCometValidators().validators.associateBy({ it.pubKey.key }, { it.votingPower })
            val matches = expectedVotingPowerByPubKey.all { (pubKey, expectedVotingPower) ->
                cometValidators[pubKey] == expectedVotingPower
            }
            if (matches) return
            Logger.info(
                "Comet validator powers not settled yet (attempt ${attempt + 1}/$maxBlocks). " +
                    "Expected=$expectedVotingPowerByPubKey Actual=$cometValidators"
            )
            genesis.node.waitForNextBlock(1)
        }

        val finalCometValidators = genesis.node.getCometValidators().validators.associateBy({ it.pubKey.key }, { it.votingPower })
        assertThat(finalCometValidators)
            .describedAs("Comet validator powers should converge before taking a validator offline")
            .containsAllEntriesOf(expectedVotingPowerByPubKey)
    }

    private fun assertRemainingValidatorsCanMaintainQuorum(
        genesis: LocalInferencePair,
        offlineValidatorPubKey: String,
    ) {
        val cometValidators = genesis.node.getCometValidators().validators
        val powersByPubKey = cometValidators.associate { validator ->
            validator.pubKey.key to checkNotNull(validator.votingPower.toLongOrNull()) {
                "Unexpected non-numeric voting power for ${validator.pubKey.key}: ${validator.votingPower}"
            }
        }
        val totalPower = powersByPubKey.values.sum()
        val remainingPower = powersByPubKey
            .filterKeys { it != offlineValidatorPubKey }
            .values
            .sum()

        assertThat(remainingPower * 3)
            .describedAs(
                "Remaining validator power should stay above two-thirds before taking a validator offline. " +
                    "Current powers=$powersByPubKey"
            )
            .isGreaterThan(totalPower * 2)
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
     * Find a future maintenance window that is safely inside inference and far
     * enough from the next PoC transition to avoid phase-overlap rejections.
     */
    private fun findSchedulableWindowStart(
        genesis: LocalInferencePair,
        participantAddress: String,
        durationBlocks: Long,
        extraLeadBuffer: Long = 1,
        maxBlockAttempts: Int = 80,
        maxExtraOffset: Long = extraLeadBuffer,
    ): Long {
        val params = getMaintenanceParams(genesis)
        repeat(maxBlockAttempts) {
            val epochData = genesis.getEpochData()
            for (offset in extraLeadBuffer..maxExtraOffset) {
                val startHeight = epochData.blockHeight + params.maintenanceMinScheduleLeadBlocks + offset

                Logger.info(
                    "Evaluating maintenance window candidate: start=$startHeight duration=$durationBlocks " +
                        "currentHeight=${epochData.blockHeight} phase=${epochData.phase} offset=$offset"
                )

                val schedulability: MaintenanceSchedulabilityResponse? = try {
                    querySchedulability(genesis, participantAddress, startHeight, durationBlocks)
                } catch (e: Exception) {
                    Logger.warn(e) { "Transient failure while querying schedulability for start=$startHeight duration=$durationBlocks" }
                    null
                }

                if (schedulability?.schedulable == true) {
                    return startHeight
                }
                if (schedulability == null) {
                    Logger.info("Candidate schedulability unavailable yet, retrying on next block")
                } else {
                    Logger.info("Candidate rejected: ${schedulability.rejectionReason}")
                }
            }

            waitForObservedBlockAdvance(genesis, epochData.blockHeight)
        }

        error("Unable to find a schedulable maintenance window for duration=$durationBlocks")
    }

    private fun waitForObservedBlockAdvance(
        genesis: LocalInferencePair,
        previousHeight: Long,
        maxPollAttempts: Int = 30,
    ) {
        repeat(maxPollAttempts) {
            try {
                val currentHeight = genesis.getCurrentBlockHeight()
                if (currentHeight > previousHeight) {
                    return
                }
            } catch (e: Exception) {
                Logger.warn(e) { "Transient failure while waiting for block advance after height $previousHeight" }
            }
            Thread.sleep(1000)
        }

        error("Observed block height did not advance past $previousHeight")
    }

    /**
     * Find a future window that deliberately overlaps the next PoC boundary.
     */
    private fun findPocOverlapWindowStart(
        genesis: LocalInferencePair,
        durationBlocks: Long,
        maxBlockAttempts: Int = 80,
    ): Long {
        val params = getMaintenanceParams(genesis)
        repeat(maxBlockAttempts) {
            val epochData = genesis.getEpochData()
            val nextPocStart = genesis.getNextStage(EpochStage.START_OF_POC)
            val earliestAllowedStart = epochData.blockHeight + params.maintenanceMinScheduleLeadBlocks
            val overlapStart = maxOf(earliestAllowedStart, nextPocStart - durationBlocks + 1)

            Logger.info(
                "Evaluating PoC-overlap window candidate: start=$overlapStart duration=$durationBlocks " +
                    "currentHeight=${epochData.blockHeight} nextPoC=$nextPocStart"
            )

            if (overlapStart < nextPocStart && overlapStart + durationBlocks > nextPocStart) {
                return overlapStart
            }

            genesis.node.waitForNextBlock(1)
        }

        error("Unable to find a deterministic PoC-overlap maintenance window")
    }

    /**
     * Query schedulability for a proposed maintenance window.
     */
    private fun querySchedulability(
        genesis: LocalInferencePair,
        participantAddress: String,
        startHeight: Long,
        durationBlocks: Long,
    ): MaintenanceSchedulabilityResponse? {
        return genesis.node.execAndParse(
            listOf(
                "query", "inference", "maintenance-schedulability",
                participantAddress,
                startHeight.toString(),
                durationBlocks.toString()
            )
        )
    }

    /**
     * Assert that the given validator is still bonded in the active validator set.
     */
    private fun assertValidatorBonded(genesis: LocalInferencePair, validatorPubKey: String, message: String) {
        val validator = checkNotNull(
            genesis.node.getValidators().validators.find { it.consensusPubkey.value == validatorPubKey }
        ) { "Validator with pubkey $validatorPubKey not found in validator set" }
        assertThat(validator.statusEnum)
            .describedAs(message)
            .isEqualTo(StakeValidatorStatus.BONDED)
    }

    @Test
    @Tag("maintenance")
    fun `maintenance scheduling semantics are deterministic`() {
        val (cluster, genesis) = initCluster()

        val params = getMaintenanceParams(genesis)
        assertThat(params.maintenanceEnabled).isTrue()

        logSection("Earning maintenance credit")
        genesis.waitForNextEpoch()
        genesis.waitForNextEpoch()

        val join1 = cluster.joinPairs[0]
        val join1Address = join1.node.getColdAddress()
        val requiredCredit = 20L
        val availableCredit = awaitMinimumCredit(genesis, join1Address, requiredCredit)
        assertThat(availableCredit).isGreaterThanOrEqualTo(requiredCredit)

        logSection("Verifying zero concurrency before any maintenance is active")
        val initialConcurrency: MaintenanceConcurrencyResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-concurrency", genesis.getCurrentBlockHeight().toString())
        )
        assertThat(initialConcurrency.concurrentCount).isEqualTo(0)

        logSection("Verifying PoC-overlapping window is rejected")
        val overlapDuration = 3L
        val overlapStart = findPocOverlapWindowStart(genesis, overlapDuration)
        val overlapSchedulability = checkNotNull(
            querySchedulability(genesis, join1Address, overlapStart, overlapDuration)
        ) { "overlap schedulability query returned null" }
        assertThat(overlapSchedulability.schedulable)
            .describedAs("Window overlapping the next PoC transition should be rejected")
            .isFalse()
        assertThat(overlapSchedulability.rejectionReason).isNotBlank()

        logSection("Finding a schedulable future window")
        val durationBlocks = 3L
        val startHeight = findSchedulableWindowStart(
            genesis,
            join1Address,
            durationBlocks,
            extraLeadBuffer = 3,
        )
        val creditBeforeSchedule: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        val schedulability = checkNotNull(
            querySchedulability(genesis, join1Address, startHeight, durationBlocks)
        ) { "schedulability query returned null for chosen future window" }
        assertThat(schedulability.schedulable).isTrue()

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

        val creditAfterSchedule: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        assertThat(creditAfterSchedule.creditBlocks)
            .describedAs("Scheduling should reserve credit equal to the window duration")
            .isEqualTo(creditBeforeSchedule.creditBlocks - durationBlocks)

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

        val creditAfterCancel: MaintenanceCreditResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-credit", join1Address)
        )
        assertThat(creditAfterCancel.creditBlocks)
            .describedAs("Credit should be restored after cancellation")
            .isGreaterThanOrEqualTo(creditBeforeSchedule.creditBlocks)

        val statusAfterCancel: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        assertThat(statusAfterCancel.scheduledReservation).isNull()
        assertThat(statusAfterCancel.activeReservation).isNull()

        val finalConcurrency: MaintenanceConcurrencyResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-concurrency", genesis.getCurrentBlockHeight().toString())
        )
        assertThat(finalConcurrency.concurrentCount).isEqualTo(0)

        genesis.markNeedsReboot()
    }

    @Test
    @Tag("maintenance")
    fun `maintenance lifecycle survives offline validator during maintenance`() {
        val (cluster, genesis) = initCluster(joinCount = 3, reboot = true)

        val params = getMaintenanceParams(genesis)
        assertThat(params.maintenanceEnabled).isTrue()

        logSection("Earning maintenance credit")
        genesis.waitForNextEpoch()
        genesis.waitForNextEpoch()

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]
        val join3 = cluster.joinPairs[2]
        val join1Address = join1.node.getColdAddress()
        val join1DefaultNode = join1.api.getNodes().first {
            it.node.host == "ml-0000.${join1.name.trimStart('/')}.test"
        }.node
        val genesisValidatorPubKey = genesis.node.getValidatorInfo().key
        val validatorPubKey = join1.node.getValidatorInfo().key
        val join2ValidatorPubKey = join2.node.getValidatorInfo().key
        val join3ValidatorPubKey = join3.node.getValidatorInfo().key
        val requiredCredit = 10L
        val creditBlocks = awaitMinimumCredit(genesis, join1Address, requiredCredit)
        assertThat(creditBlocks).isGreaterThanOrEqualTo(requiredCredit)

        logSection("Reducing join1 voting power so the cluster stays live while join1 is offline")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        join1.setPocWeight(5, join1DefaultNode)
        join1.waitForNextEpoch()
        join1.node.waitForNextBlock(1)
        val reducedJoin1Validator = join1.node.getValidators().validators.first {
            it.consensusPubkey.value == validatorPubKey
        }
        assertThat(reducedJoin1Validator.tokens)
            .describedAs("join1 voting power should be reduced before taking it offline")
            .isEqualTo(5)
        join1.node.waitForNextBlock(1)
        val reducedJoin1CometValidator = genesis.node.getCometValidators().validators.first {
            it.pubKey.key == validatorPubKey
        }
        assertThat(reducedJoin1CometValidator.votingPower)
            .describedAs("join1 comet voting power should be reduced before taking it offline")
            .isEqualTo("5")
        waitForCometValidatorPowers(
            genesis,
            mapOf(
                genesisValidatorPubKey to "10",
                validatorPubKey to "5",
                join2ValidatorPubKey to "10",
                join3ValidatorPubKey to "10",
            )
        )

        val durationBlocks = 6L
        logSection("Scheduling a future maintenance window")
        val startHeight = findSchedulableWindowStart(
            genesis,
            join1Address,
            durationBlocks,
            extraLeadBuffer = 5,
            maxBlockAttempts = 200,
            maxExtraOffset = 80,
        )
        val scheduleTx = join1.submitTransaction(
            listOf(
                "inference", "schedule-maintenance",
                "--participant", join1Address,
                "--start-height", startHeight.toString(),
                "--duration-blocks", durationBlocks.toString(),
            )
        )
        assertThat(scheduleTx.code).isEqualTo(0)

        val scheduledStatus: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        val scheduledReservation = checkNotNull(scheduledStatus.scheduledReservation) {
            "Scheduled reservation missing after maintenance scheduling"
        }
        assertThat(scheduledReservation.startHeight).isEqualTo(startHeight)
        assertThat(scheduledReservation.durationBlocks).isEqualTo(durationBlocks)

        assertValidatorBonded(
            genesis,
            validatorPubKey,
            "Validator should be bonded before maintenance activation"
        )

        logSection("Waiting for maintenance activation")
        genesis.node.waitForMinimumBlock(startHeight + 1, "maintenance activation")

        val activeResponse: MaintenanceActiveResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-active")
        )
        assertThat(activeResponse.reservations).hasSize(1)
        assertThat(activeResponse.reservations.single().participant).isEqualTo(join1Address)

        val activeConcurrency: MaintenanceConcurrencyResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-concurrency", genesis.getCurrentBlockHeight().toString())
        )
        assertThat(activeConcurrency.concurrentCount).isEqualTo(1)

        val endHeight = startHeight + durationBlocks
        val currentHeightBeforeStop = genesis.getCurrentBlockHeight()
        assertThat(currentHeightBeforeStop)
            .describedAs("Maintenance window should still have runway left before taking join1 offline")
            .isLessThan(endHeight - 1)
        assertRemainingValidatorsCanMaintainQuorum(genesis, validatorPubKey)

        logSection("Stopping join1 chain node during active maintenance")
        stopNodeContainer(join1)

        logSection("Waiting for the maintenance window to complete")
        genesis.node.waitForMinimumBlock(endHeight + 2, "maintenance completion")

        assertValidatorBonded(
            genesis,
            validatorPubKey,
            "Validator should remain bonded after being offline during maintenance"
        )

        val statusAfterMaintenance: MaintenanceStatusResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-status", join1Address)
        )
        assertThat(statusAfterMaintenance.activeReservation).isNull()
        assertThat(statusAfterMaintenance.scheduledReservation).isNull()

        val activeAfterMaintenance: MaintenanceActiveResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-active")
        )
        assertThat(activeAfterMaintenance.reservations.none { it.participant == join1Address }).isTrue()

        val concurrencyAfterMaintenance: MaintenanceConcurrencyResponse = genesis.node.execAndParse(
            listOf("query", "inference", "maintenance-concurrency", genesis.getCurrentBlockHeight().toString())
        )
        assertThat(concurrencyAfterMaintenance.concurrentCount).isEqualTo(0)

        genesis.markNeedsReboot()
    }
}
