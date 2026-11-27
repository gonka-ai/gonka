import com.productscience.*
import com.productscience.data.DevshardInferenceStatus
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import java.util.concurrent.Executors
import kotlin.test.assertNotNull

/**
 * Heavier / special-config versiond → devshardd coverage.
 *
 * Split from DevshardStandaloneTests so CI can run this class in parallel with
 * [DevshardVersiondSessionTests].
 */
class DevshardVersiondAdvancedTests : DevshardVersiondTestBase() {

    @Test
    fun `devshard gateway auto-seals inferences after grace timeout via devshardd`() {
        val slots = devshardAutoSealGroupSize.toInt()
        val firstBatch = slots * 2

        val (cluster, genesis) = initCluster(config = shortSealGraceConfig, reboot = true)
        genesis.waitForNextEpoch()
        waitForOverrideVersionedHealth(genesis)
        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshardd-autoseal-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Serving the auto-seal escrow through the gateway")
        val handle = genesis.serveDevshardEscrow(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardEscrowWarmup(handle.escrowUrl)

            val status = genesis.getDevshardEscrowStatus(handle.escrowUrl)
            assertThat(status.config.inferenceSealGraceNonces).isEqualTo(devshardAutoSealInferenceSealGraceNonces.toInt())
            assertThat(status.config.inferenceSealGraceSeconds)
                .isEqualTo(devshardAutoSealInferenceSealGraceSeconds.toInt())
            assertThat(status.config.executionTimeout).isEqualTo(devshardAutoSealExecutionTimeout)
            // Governance/create treats validation_rate=0 as unset → default 1000 (10%).
            assertThat(status.config.validationRate).isEqualTo(1_000)

            logSection("Sending first batch ($firstBatch finished inferences)")
            for (i in 0 until firstBatch) {
                val response = genesis.sendChatCompletion(handle.escrowUrl, defaultModel, "autoseal batch1 $i")
                assertThat(response).isNotEmpty()
            }
            genesis.waitForFinishedDevshardInferences(handle.escrowUrl, firstBatch)

            val debugBeforeGrace = genesis.getDevshardEscrowDebugState(handle.escrowUrl)
            val statusCountsBefore = genesis.devshardStatusCounts(handle.escrowUrl)
            // The gateway lists sealed records too, so every sealed one is taken off the finished count.
            val liveFinishedBefore =
                (statusCountsBefore[DevshardInferenceStatus.FINISHED] ?: 0) - debugBeforeGrace.sealedInferences
            logSection(
                "Before grace wait: live=${debugBeforeGrace.liveInferences} " +
                    "sealed=${debugBeforeGrace.sealedInferences} nonce=${debugBeforeGrace.nonce} " +
                    "status_counts=$statusCountsBefore",
            )
            assertThat(debugBeforeGrace.liveInferences).isGreaterThanOrEqualTo(firstBatch)
            // Finished still needs the clock gate (grace + execution_timeout).
            // Validated/Invalidated/TimedOut skip that gate and may already be sealed.
            assertThat(liveFinishedBefore)
                .describedAs("Finished inferences must still be live before the clock gate clears")
                .isGreaterThanOrEqualTo(firstBatch)

            val finishedClockSeconds =
                devshardAutoSealInferenceSealGraceSeconds + devshardAutoSealExecutionTimeout
            logSection("Waiting ${finishedClockSeconds}s Finished clock gate (grace + execution timeout)")
            Thread.sleep((finishedClockSeconds + 2) * 1_000L)

            logSection(
                "Driving nonce to auto-seal boundary ($devshardAutoSealEveryNNonces) " +
                    "and waiting for >= $firstBatch sealed inferences",
            )
            genesis.waitForDevshardAutoSeal(
                escrowUrl = handle.escrowUrl,
                minSealed = firstBatch,
                targetNonce = devshardAutoSealEveryNNonces,
            )

            val debugAfter = genesis.getDevshardEscrowDebugState(handle.escrowUrl)
            logSection(
                "After second batch: live=${debugAfter.liveInferences} " +
                    "sealed=${debugAfter.sealedInferences} nonce=${debugAfter.nonce} " +
                    "status_counts=${genesis.devshardStatusCounts(handle.escrowUrl)}",
            )

            assertThat(debugAfter.sealedInferences)
                .describedAs("gateway should auto-seal Finished inferences after grace + new nonce")
                .isGreaterThan(debugBeforeGrace.sealedInferences)
            assertThat(debugAfter.sealedInferences)
                .describedAs("at least the first batch of Finished inferences should seal")
                .isGreaterThanOrEqualTo(firstBatch)
            assertThat(debugAfter.liveInferences)
                .describedAs("live map should shrink as sealed inferences are folded into sealed_acc")
                .isLessThan(debugBeforeGrace.liveInferences)
        } finally {
            genesis.withdrawDevshardEscrow(escrowId)
        }
    }

    @Test
    fun `state-approved devshardd seeded at startup is downloaded without overrides`() {
        assertNoVersiondOverrides(stateDrivenVersiondEnv)

        val preparedArtifact = prepareReleaseStyleDevsharddArtifact()
        val (cluster, genesis) = initCluster(
            config = stateDrivenConfig(preparedArtifact.approvedVersion),
            reboot = true,
        )
        genesis.waitForNextEpoch()

        logSection("Waiting for devshardd artifact server readiness")
        val servedSha256 = waitForDevsharddArtifactSha(genesis)
        assertThat(servedSha256).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Verifying chain store contains ${preparedArtifact.approvedVersion.name} at startup")
        val approvedVersions = genesis.getDevshardApprovedVersions()
        val approvedVersion = approvedVersions.single { it.name == preparedArtifact.approvedVersion.name }
        assertThat(approvedVersion.binary).isEqualTo(preparedArtifact.approvedVersion.binary)
        assertThat(approvedVersion.sha256).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Waiting for dapi /versions to expose ${preparedArtifact.approvedVersion.name}")
        waitUntil("dapi serves ${preparedArtifact.approvedVersion.name}", timeoutSeconds = 60) {
            getDapiVersions(genesis).any { it["name"] == preparedArtifact.approvedVersion.name }
        }
        val dapiVersion = getDapiVersions(genesis).single { it["name"] == preparedArtifact.approvedVersion.name }
        assertThat(dapiVersion["binary"]).isEqualTo(preparedArtifact.approvedVersion.binary)
        assertThat(dapiVersion["sha256"]).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Waiting for every pair to download ${preparedArtifact.approvedVersion.name}")
        waitUntil("downloaded binary and install metadata exist on every pair", timeoutSeconds = 120) {
            cluster.allPairs.all { pair ->
                pair.versiondBinaryExists(
                    preparedArtifact.approvedVersion.name,
                    "devshardd",
                    preparedArtifact.approvedVersion.sha256,
                ) &&
                    pair.readVersiondInstallMetadata(
                        preparedArtifact.approvedVersion.name,
                        preparedArtifact.approvedVersion.sha256,
                    )?.archiveSha256 ==
                    preparedArtifact.approvedVersion.sha256
            }
        }
        cluster.allPairs.forEach { pair ->
            assertThat(
                pair.versiondBinaryExists(
                    preparedArtifact.approvedVersion.name,
                    "devshardd",
                    preparedArtifact.approvedVersion.sha256,
                )
            )
                .withFailMessage(
                    "Expected ${pair.name} versiond to download " +
                        pair.versiondBinaryPath(
                            preparedArtifact.approvedVersion.name,
                            "devshardd",
                            preparedArtifact.approvedVersion.sha256,
                        ),
                )
                .isTrue()
            val installMetadata = assertNotNull(
                pair.readVersiondInstallMetadata(
                    preparedArtifact.approvedVersion.name,
                    preparedArtifact.approvedVersion.sha256,
                )
            )
            assertThat(installMetadata.archiveSha256).isEqualTo(preparedArtifact.approvedVersion.sha256)
            assertThat(installMetadata.binarySha256).isNotBlank()
        }

        logSection("Waiting for another versiond poll cycle to confirm stability")
        Thread.sleep(Duration.ofSeconds(7).toMillis())
        cluster.allPairs.forEach { pair ->
            val stableLogs = pair.readVersiondLogs(tail = 800)
            assertThat(stableLogs)
                .withFailMessage("Expected ${pair.name} versiond logs to stay stable after download.\n$stableLogs")
                .doesNotContain("hash mismatch on running version")
                .doesNotContain("installed archive hash mismatch")
                .doesNotContain("installed binary hash mismatch")
        }

        logSection("Verifying versioned health route through proxy")
        waitUntil("proxy serves ${preparedArtifact.routePrefix}/healthz", timeoutSeconds = 90) {
            runCatching {
                getVersionedHealth(genesis, preparedArtifact.approvedVersion.name) == "ok"
            }.getOrDefault(false)
        }
        assertThat(getVersionedHealth(genesis, preparedArtifact.approvedVersion.name)).isEqualTo("ok")
    }

    @Test
    fun `parallel devshard sessions with isolated settlement via devshardd`() {
        val sessionCount = 6
        val (cluster, genesis) = initCluster(config = parallelLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        data class UserInfo(val keyName: String, val address: String)
        data class SessionSetup(val keyName: String, val address: String, val escrowId: Long)

        val fundAmount = 10_000_000_000L
        val escrowAmount = 7_000_000_000L

        val users = (0 until sessionCount).map { i ->
            val user = genesis.createFundedDevshardUser("devshardd-parallel-$i", fundAmount)
            UserInfo(user.keyName, user.address)
        }

        genesis.waitForNextEpoch()
        genesis.waitForNextInferenceWindow()

        val sessions = users.mapIndexed { i, user ->
            logSection("Creating escrow for user $i")
            val escrowId =
                genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
            SessionSetup(user.keyName, user.address, escrowId)
        }

        logSection("Starting $sessionCount devshard proxies against devshardd")
        val handles = sessions.map { session ->
            genesis.serveDevshardEscrow(
                escrowId = session.escrowId,
                keyName = session.keyName,
                routePrefix = overrideRoutePrefix,
            )
        }

        try {
            handles.forEach { genesis.waitForDevshardEscrowWarmup(it.escrowUrl) }
            logSection("Running $sessionCount gateway escrows in parallel")
            val dispatcher = Executors.newFixedThreadPool(sessionCount).asCoroutineDispatcher()
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.escrowUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.escrowUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }

            logSection("Syncing devshard hosts before validation observability")
            handles.forEach { handle ->
                genesis.syncDevshardEscrowHosts(handle.escrowUrl)
            }

            logSection("Waiting for validation observability on active escrows")
            // Re-sync on each retry: validations often finish after the initial
            // sync-hosts, and obs only appears on genesis after those diffs apply.
            sessions.zip(handles).forEach { (session, handle) ->
                genesis.waitForDevshardValidationObservability(
                    session.escrowId,
                    minCompleted = 1,
                    routePrefix = overrideRoutePrefix,
                    beforeRetry = {
                        genesis.syncDevshardEscrowHosts(handle.escrowUrl, log = false)
                    },
                )
            }

            logSection("Finalizing, settling, and verifying $sessionCount escrows")
            sessions.zip(handles).forEach { (session, handle) ->
                val obs = genesis.getDevshardShardStatsDetail(session.escrowId, routePrefix = overrideRoutePrefix)
                assertThat(obs.validationObservability.totals.completedValidations)
                    .withFailMessage("validation observability for escrow ${session.escrowId}")
                    .isGreaterThan(0)

                val result = genesis.settleDevshardThroughGateway(session.escrowId)
                val settlement = result.settlement
                val settleTransaction = result.settleTx
                assertThat(settleTransaction.code)
                    .withFailMessage("Settlement failed for escrow ${session.escrowId}")
                    .isEqualTo(0)
                assertThat(result.settler).isEqualTo(session.address)
                assertThat(settlement.escrowId)
                    .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                    .isEqualTo(session.escrowId.toString())
                assertThat(settlement.stateRootAndProtocolVersion).isEqualTo(devshardStateRootProtocolVersion())
                assertThat(settlement.hostStats).isNotEmpty()
                // Settlement host_stats validation counters are always zero on this
                // branch: the reveal-based recomputeCompliance was removed with seed
                // reveal (see devshard/docs/inference-lifecycle.md). Validation
                // coverage is asserted via validationObservability below instead.
                assertThat(settlement.signatures).isNotEmpty()
                val settleEvent = assertNotNull(settleTransaction.events.firstOrNull { it.type == "devshard_escrow_settled" })
                assertThat(
                    settleEvent.attributes.firstOrNull { it.key == "state_root_and_protocol_version" }?.value,
                ).isEqualTo(devshardStateRootProtocolVersion())

                val escrow = genesis.node.queryDevshardEscrow(session.escrowId)
                assertThat(escrow.escrow!!.settled)
                    .withFailMessage("Escrow ${session.escrowId} not settled")
                    .isTrue()

                val balance = genesis.getBalance(session.address)
                assertThat(balance)
                    .withFailMessage("User ${session.keyName} did not receive refund")
                    .isGreaterThan(fundAmount - escrowAmount)
            }
        } finally {
            handles.forEach { genesis.withdrawDevshardEscrow(it.escrowId) }
        }
    }

    @Test
    fun `invalid inference is challenged via devshardd`() {
        val (cluster, genesis) = initCluster(config = overrideAlwaysValidateConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(
                response = defaultInferenceResponseObject,
                streamDelay = Duration.ofMillis(50),
            )
        }
        cluster.allPairs.last().mock?.stubDevshardResponseForAllSegments(
            response = defaultInferenceResponseObject.withMissingLogit(),
        )

        val user = genesis.createFundedDevshardUser("devshardd-challenged-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Serving the escrow through the gateway")
        val handle = genesis.serveDevshardEscrow(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardEscrowWarmup(handle.escrowUrl)
            logSection("Sending chat completions through the gateway")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.escrowUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.waitForDevshardPreFinalize()
            logSection("Settling through the gateway")
            val result = genesis.settleDevshardThroughGateway(escrowId)
            val settlement = result.settlement
            val settleTransaction = result.settleTx
            assertThat(settleTransaction.code).isEqualTo(0)
            assertThat(result.settler).isEqualTo(user.address)

            logSection("Verifying settlement data")
            assertThat(settlement.escrowId).isEqualTo("$escrowId")
            assertThat(settlement.stateRootAndProtocolVersion).isEqualTo(devshardStateRootProtocolVersion())
            assertThat(settlement.nonce).isGreaterThan(0)
            assertThat(settlement.hostStats).isNotEmpty()
            assertThat(settlement.signatures).isNotEmpty()
            val settleEvent = assertNotNull(settleTransaction.events.firstOrNull { it.type == "devshard_escrow_settled" })
            assertThat(
                settleEvent.attributes.firstOrNull { it.key == "state_root_and_protocol_version" }?.value,
            ).isEqualTo(devshardStateRootProtocolVersion())

            logSection("Verifying escrow settled")
            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            assertThat(escrow.escrow!!.settled).isTrue()

            logSection("Verifying inference status")
            val inference = assertNotNull(genesis.findChallengedDevshardInference(handle))
            logSection("Inference: $inference")
            assertThat(inference.status).isIn(
                DevshardInferenceStatus.CHALLENGED,
                DevshardInferenceStatus.INVALIDATED,
            )
            assertThat(inference.votesInvalid).isNotZero()
        } finally {
            genesis.withdrawDevshardEscrow(escrowId)
        }
    }
}
