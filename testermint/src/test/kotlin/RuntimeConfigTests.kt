import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.NodeManagerClient
import com.productscience.createSpec
import com.productscience.data.AppState
import com.productscience.data.EpochPhase
import com.productscience.data.GovParams
import com.productscience.data.GovState
import com.productscience.data.UpdateParams
import com.productscience.data.spec
import com.productscience.initCluster
import com.productscience.inferenceConfig
import com.productscience.logSection
import com.productscience.nodemanager.NodeManagerProto
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestInstance
import org.junit.jupiter.api.TestMethodOrder
import java.time.Duration
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit
import kotlin.system.measureTimeMillis

/**
 * Step 3b-T: dapi NodeManager `GetRuntimeConfig` (immediate + long-poll).
 *
 * One genesis cluster for the whole class ([BeforeAll] / [AfterAll] `markNeedsReboot`).
 * No versiond/devshardd — direct gRPC from the test process only.
 *
 * Host-path coverage: [DevsharddRuntimeConfigTests] (governance 503, epoch, dapi restart).
 * Do not duplicate long-poll wake cases there.
 *
 * Genesis: `epoch_length=15`, `voting_period=12s` (~one epoch per vote window).
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class RuntimeConfigTests : TestermintTest() {

    private val runtimeConfigSpec = createSpec(epochLength = 15L).merge(
        spec<AppState> {
            this[AppState::gov] = spec<GovState> {
                this[GovState::params] = spec<GovParams> {
                    // Default 30s vote window exceeds one epoch (~15 blocks) and races long-poll tests.
                    this[GovParams::votingPeriod] = Duration.ofSeconds(12)
                }
            }
        }
    )

    private val runtimeConfigConfig = inferenceConfig.copy(genesisSpec = runtimeConfigSpec)

    private lateinit var cluster: LocalCluster
    private lateinit var genesis: LocalInferencePair

    @BeforeAll
    fun setupCluster() {
        val (c, g) = initCluster(joinCount = 0, config = runtimeConfigConfig, reboot = true)
        cluster = c
        genesis = g
        nodeManagerClient(genesis).use { waitForSyncedRuntimeConfig(it) }
    }

    @AfterAll
    fun teardownCluster() {
        if (::genesis.isInitialized) {
            genesis.markNeedsReboot()
        }
    }

    /**
     * Avoid starting a long-poll near the next PoC start (epoch transition wakes runtime-config waiters).
     * Uses [EpochResponse.nextEpochStages.pocStart] like [EpochResponse.safeForInference].
     */
    private fun LocalInferencePair.waitForMidEpochWindow(
        minBlocksIntoEpoch: Long = 3,
        minBlocksBeforeNextPoc: Long = 4,
    ) {
        repeat(60) { attempt ->
            val epoch = getEpochData()
            val height = getCurrentBlockHeight()
            val pocStart = epoch.latestEpoch.pocStartBlockHeight
            val nextPocStart = epoch.nextEpochStages.pocStart
            val blocksInto = height - pocStart
            val blocksUntilNextPoc = nextPocStart - height
            if (epoch.phase == EpochPhase.Inference &&
                blocksInto >= minBlocksIntoEpoch &&
                blocksUntilNextPoc >= minBlocksBeforeNextPoc
            ) {
                return
            }
            Thread.sleep(2_000)
            if (attempt == 59) {
                error(
                    "timed out waiting for mid-epoch window: height=$height phase=${epoch.phase} " +
                        "pocStart=$pocStart nextPocStart=$nextPocStart blocksInto=$blocksInto " +
                        "blocksUntilNextPoc=$blocksUntilNextPoc"
                )
            }
        }
    }

    private fun nodeManagerClient(pair: LocalInferencePair): NodeManagerClient {
        val port = pair.nodeManagerGrpcHostPort
            ?: error("NodeManager gRPC port not available for ${pair.name}")
        return NodeManagerClient("localhost", port)
    }

    private fun waitForSyncedRuntimeConfig(client: NodeManagerClient): NodeManagerProto.RuntimeConfig {
        var clientHeight = 0L
        repeat(60) { attempt ->
            val resp = client.getRuntimeConfig(clientParamsBlockHeight = clientHeight, maxWaitSeconds = 0)
            if (!resp.unchanged && resp.hasConfig()) {
                clientHeight = resp.config.paramsBlockHeight
                if (clientHeight > 0) {
                    return resp.config
                }
            }
            Thread.sleep(5_000)
            if (attempt == 59) {
                error(
                    "dapi runtime config never synced (params_block_height still 0 after 5m): " +
                        "unchanged=${resp.unchanged} hasConfig=${resp.hasConfig()}"
                )
            }
        }
        error("unreachable")
    }

    /** Stable params_block_height + epoch; optional mid-epoch guard for idle long-polls (tests 3–4). */
    private fun waitForStableRuntimeRevision(
        client: NodeManagerClient,
        pair: LocalInferencePair? = null,
    ): NodeManagerProto.RuntimeConfig {
        pair?.waitForMidEpochWindow()
        var prev = waitForSyncedRuntimeConfig(client)
        repeat(15) {
            Thread.sleep(2_000)
            val resp = client.getRuntimeConfig(clientParamsBlockHeight = prev.paramsBlockHeight, maxWaitSeconds = 0)
            check(!resp.unchanged && resp.hasConfig()) {
                "expected full runtime config while stabilizing revision"
            }
            val cur = resp.config
            if (cur.paramsBlockHeight == prev.paramsBlockHeight && cur.currentEpochId == prev.currentEpochId) {
                return prev
            }
            prev = cur
        }
        error(
            "runtime revision did not stabilize within 30s " +
                "(height=${prev.paramsBlockHeight} epoch=${prev.currentEpochId})"
        )
    }

    private fun chainEscrow(pair: LocalInferencePair) =
        pair.getParams().devshardEscrowParams
            ?: error("devshard_escrow_params missing from chain params")

    private fun runLongPollWakeTest(
        client: NodeManagerClient,
        atHeight: Long,
        maxWaitSeconds: Int = 60,
        maxElapsedMs: Long = 30_000,
        trigger: () -> Unit,
        verifyResponse: (NodeManagerProto.GetRuntimeConfigResponse) -> Unit,
    ) {
        val result = CompletableFuture<NodeManagerProto.GetRuntimeConfigResponse>()
        val pollThread = Thread {
            result.complete(
                client.getRuntimeConfig(clientParamsBlockHeight = atHeight, maxWaitSeconds = maxWaitSeconds)
            )
        }
        pollThread.start()
        Thread.sleep(500)

        // Run trigger concurrently — waitForNextEpoch() can exceed maxWaitSeconds.
        val triggerThread = Thread { trigger() }
        triggerThread.start()

        val resultTimeoutSeconds = maxWaitSeconds.toLong() + 15L
        val elapsed = measureTimeMillis {
            val resp = result.get(resultTimeoutSeconds, TimeUnit.SECONDS)
            pollThread.join(5_000)
            triggerThread.join(resultTimeoutSeconds * 2)
            assertThat(resp.unchanged).isFalse()
            assertThat(resp.hasConfig()).isTrue()
            assertThat(resp.config.paramsBlockHeight).isGreaterThan(atHeight)
            verifyResponse(resp)
        }
        assertThat(elapsed).isLessThan(maxElapsedMs)
    }

    private fun NodeManagerProto.GetRuntimeConfigResponse.configOrNull() =
        if (hasConfig()) config else null

    @Test
    @Order(1)
    fun `initial fetch returns runtime config after chain sync`() {
        nodeManagerClient(genesis).use { client ->
            val cfg = waitForSyncedRuntimeConfig(client)
            val chain = genesis.getParams()

            assertThat(cfg.currentEpochId).isGreaterThanOrEqualTo(0)
            assertThat(cfg.logprobsMode).isEqualTo(chain.validationParams.logprobsMode)

            val escrow = chainEscrow(genesis)
            assertThat(cfg.devshardRequestsEnabled).isEqualTo(escrow.devshardRequestsEnabled)
            assertThat(cfg.defaultSealGraceNonces).isEqualTo(escrow.defaultSealGraceNonces.toInt())
            assertThat(cfg.defaultInferenceClearGraceSeconds)
                .isEqualTo(escrow.defaultInferenceClearGraceSeconds.toInt())
            assertThat(cfg.maxNonce).isEqualTo(escrow.maxNonce.toInt())

            val chainVersions = escrow.approvedVersions.orEmpty()
            assertThat(cfg.approvedVersionsList.map { it.name })
                .containsExactlyElementsOf(chainVersions.map { it.name })
        }
    }

    @Test
    @Order(2)
    fun `legacy client gets immediate unchanged`() {
        nodeManagerClient(genesis).use { client ->
            val synced = waitForStableRuntimeRevision(client)
            val height = synced.paramsBlockHeight

            val legacyElapsed = measureTimeMillis {
                val resp = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = null)
                assertThat(resp.unchanged).isTrue()
                assertThat(resp.hasConfig()).isFalse()
            }
            assertThat(legacyElapsed).isLessThan(1_000)

            val explicitZeroElapsed = measureTimeMillis {
                val resp = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 0)
                assertThat(resp.unchanged).isTrue()
                assertThat(resp.hasConfig()).isFalse()
            }
            assertThat(explicitZeroElapsed).isLessThan(1_000)
        }
    }

    @Test
    @Order(3)
    fun `long poll times out when chain is idle`() {
        nodeManagerClient(genesis).use { client ->
            val synced = waitForStableRuntimeRevision(client, genesis)
            val height = synced.paramsBlockHeight
            val epochAtStart = synced.currentEpochId

            val elapsed = measureTimeMillis {
                val resp = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 3)
                assertThat(resp.unchanged)
                    .withFailMessage(
                        "expected timeout without revision change; got height ${resp.configOrNull()?.paramsBlockHeight} " +
                            "epoch ${resp.configOrNull()?.currentEpochId} (started at $height / $epochAtStart)"
                    )
                    .isTrue()
                assertThat(resp.hasConfig()).isFalse()
            }
            assertThat(elapsed).isGreaterThanOrEqualTo(2_000)
            assertThat(elapsed).isLessThan(10_000)

            val after = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 0)
            assertThat(after.unchanged).isTrue()
            assertThat(after.hasConfig()).isFalse()
        }
    }

    @Test
    @Order(4)
    fun `long poll does not wake on blocks without runtime config change`() {
        nodeManagerClient(genesis).use { client ->
            val synced = waitForStableRuntimeRevision(client, genesis)
            val height = synced.paramsBlockHeight
            val epochAtStart = synced.currentEpochId
            val chainHeightAtStart = genesis.getCurrentBlockHeight()

            val result = CompletableFuture<NodeManagerProto.GetRuntimeConfigResponse>()
            val pollThread = Thread {
                result.complete(client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 12))
            }
            pollThread.start()

            val elapsed = measureTimeMillis {
                val resp = result.get(20, TimeUnit.SECONDS)
                pollThread.join(5_000)
                assertThat(resp.unchanged)
                    .withFailMessage(
                        "long-poll woke early: revision ${height}->${resp.configOrNull()?.paramsBlockHeight} " +
                            "epoch $epochAtStart->${resp.configOrNull()?.currentEpochId}"
                    )
                    .isTrue()
                assertThat(resp.hasConfig()).isFalse()
            }
            assertThat(elapsed).isGreaterThanOrEqualTo(10_000)
            assertThat(elapsed).isLessThan(20_000)

            assertThat(genesis.getCurrentBlockHeight())
                .withFailMessage("expected chain to produce blocks during long-poll window")
                .isGreaterThan(chainHeightAtStart)

            val after = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 0)
            assertThat(after.unchanged).isTrue()
            assertThat(after.hasConfig()).isFalse()
        }
    }

    @Test
    @Order(5)
    @Tag("integration")
    fun `long poll wakes on next epoch`() {
        nodeManagerClient(genesis).use { client ->
            // Start mid-epoch so the next PoC (runtime revision) is a short wait, not a full epoch cycle.
            genesis.waitForMidEpochWindow()
            val synced = waitForStableRuntimeRevision(client)
            val height = synced.paramsBlockHeight
            val epochBefore = synced.currentEpochId
            val chainEpochBefore = genesis.getEpochData().latestEpoch.index

            runLongPollWakeTest(
                client = client,
                atHeight = height,
                maxElapsedMs = 60_000,
                trigger = {
                    logSection("Advancing epoch to bump runtime config height (epoch $epochBefore -> next)")
                    genesis.waitForNextEpoch()
                },
                verifyResponse = { resp ->
                    assertThat(resp.config.currentEpochId).isGreaterThan(epochBefore)
                },
            )

            assertThat(genesis.getEpochData().latestEpoch.index).isGreaterThan(chainEpochBefore)
            val epochAfter = client.getRuntimeConfig(clientParamsBlockHeight = height, maxWaitSeconds = 0)
            assertThat(epochAfter.unchanged).isFalse()
            assertThat(epochAfter.config.currentEpochId).isGreaterThan(epochBefore)
        }
    }

    @Test
    @Order(6)
    @Tag("integration")
    fun `long poll wakes on governance param change`() {
        nodeManagerClient(genesis).use { client ->
            // No mid-epoch guard: test 5 just advanced epoch; next PoC is ~epoch_length blocks away
            // and we assert max_nonce from governance (epoch-only wake would fail).
            val synced = waitForStableRuntimeRevision(client)
            val height = synced.paramsBlockHeight
            val chainParams = genesis.getParams()
            val escrow = chainEscrow(genesis)
            val maxNonceBefore = escrow.maxNonce

            runLongPollWakeTest(
                client = client,
                atHeight = height,
                trigger = {
                    logSection("Governance: bump devshard_escrow_params.max_nonce $maxNonceBefore -> ${maxNonceBefore + 1}")
                    val modifiedParams = chainParams.copy(
                        devshardEscrowParams = escrow.copy(maxNonce = maxNonceBefore + 1)
                    )
                    genesis.runProposal(cluster, UpdateParams(params = modifiedParams))
                },
                verifyResponse = { resp ->
                    assertThat(resp.config.maxNonce).isEqualTo((maxNonceBefore + 1).toInt())
                },
            )

            assertThat(chainEscrow(genesis).maxNonce).isEqualTo(maxNonceBefore + 1)
        }
    }
}
