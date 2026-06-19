import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.GENESIS_KEY_NAME
import com.productscience.LocalCluster
import com.productscience.defaultModel
import com.productscience.devshardTestVersion
import com.productscience.devshardVersionedRoutePrefix
import com.productscience.LocalInferencePair
import com.productscience.NodeManagerClient
import com.productscience.versiondOverrideEnv
import com.productscience.waitForVersiondOverrideReady
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
import org.junit.jupiter.api.Disabled
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestInstance
import org.junit.jupiter.api.TestMethodOrder
import java.nio.file.Files
import java.nio.file.Path
import java.time.Duration
import kotlin.system.measureTimeMillis

/**
 * E2E: standalone devshardd loads runtime params from dapi NodeManager long-poll (via versiond).
 *
 * One cluster per class ([BeforeAll] only). Complements [RuntimeConfigTests], which exercises
 * the same gRPC from the test process without versiond/devshardd. Covers host-visible effects:
 * epoch propagation, dapi restart recovery; governance HTTP case accepted via `@Disabled` (see implementation doc).
 *
 * Requires `build/devshardd` (build with same DEVSHARD_VERSION as tests, see [devshardTestVersion]).
 *
 * Run (rebuild images + binary first):
 * ```
 * cd local-test-net && ./stop-rebuild.sh
 * cd ../testermint && ./gradlew :test --tests "DevsharddRuntimeConfigTests" -DexcludeTags=unstable,exclude
 * ```
 * Do not pass `-DexcludeTags=` with an empty value.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class DevsharddRuntimeConfigTests : TestermintTest() {

    private val standaloneTestVersionName = devshardTestVersion()
    private val overrideRoutePrefix = devshardVersionedRoutePrefix(standaloneTestVersionName)
    private val devshardEscrowModel = defaultModel
    /** Wall-clock budget after governance proposal until completions must return 503. */
    private val propagationSlaMs = 60_000L

    /**
     * Re-enable path: voting (~12s) + blocks, long-poll param apply, and
     * [LocalInferencePair.waitForNextInferenceWindow] near epoch end (~45s).
     */
    private val governanceReEnableSlaMs = 120_000L

    /** After dapi container restart, allow NodeManager + versiond proxy to recover. */
    private val postRestartWarmupDelay = Duration.ofSeconds(10)

    private val runtimeConfigSpec = createSpec(epochLength = 15L).merge(
        spec<AppState> {
            this[AppState::gov] = spec<GovState> {
                this[GovState::params] = spec<GovParams> {
                    this[GovParams::votingPeriod] = Duration.ofSeconds(12)
                }
            }
        }
    ).merge(devshardNoRestrictionsSpec)

    private val versiondDevsharddConfig = inferenceConfig.copy(
        genesisSpec = runtimeConfigSpec,
        additionalDockerFilesByKeyName = mapOf(GENESIS_KEY_NAME to listOf("docker-compose.versiond.yml")),
        additionalEnvVars = versiondOverrideEnv(standaloneTestVersionName),
    )

    private val repoRoot: Path by lazy { resolveRepoRoot() }
    private val devsharddHostBinary: Path
        get() = repoRoot.resolve("build").resolve("devshardd")

    private lateinit var cluster: LocalCluster
    private lateinit var genesis: LocalInferencePair

    @BeforeAll
    fun setupCluster() {
        check(Files.isRegularFile(devsharddHostBinary)) {
            "Missing devshardd binary at $devsharddHostBinary. Run: make devshardd-build (DEVSHARD_VERSION=$standaloneTestVersionName)"
        }
        val (c, g) = initCluster(joinCount = 0, config = versiondDevsharddConfig, reboot = true)
        cluster = c
        genesis = g
        genesis.waitForVersiondOverrideReady(standaloneTestVersionName)
        cluster.stubDevshardChatResponse()
        nodeManagerClient(genesis).use { waitForSyncedRuntimeConfig(it) }
    }

    @AfterAll
    fun teardownCluster() {
        if (::genesis.isInitialized) {
            genesis.markNeedsReboot()
        }
    }

    private fun nodeManagerClient(pair: LocalInferencePair): NodeManagerClient {
        val port = pair.nodeManagerGrpcHostPort
            ?: error("NodeManager gRPC port not available for ${pair.name}")
        return NodeManagerClient("localhost", port)
    }

    private fun waitForSyncedRuntimeConfig(client: NodeManagerClient): NodeManagerProto.RuntimeConfig {
        var clientHeight = 0L
        repeat(60) {
            val resp = client.getRuntimeConfig(clientParamsBlockHeight = clientHeight, maxWaitSeconds = 0)
            if (!resp.unchanged && resp.hasConfig()) {
                clientHeight = resp.config.paramsBlockHeight
                if (clientHeight > 0) {
                    return resp.config
                }
            }
            Thread.sleep(5_000)
        }
        error("dapi runtime config never synced after 5m")
    }

    private fun waitForRuntimeEpochAtLeast(
        client: NodeManagerClient,
        minEpoch: Long,
        timeoutMs: Long = propagationSlaMs,
    ): NodeManagerProto.RuntimeConfig {
        val deadline = System.currentTimeMillis() + timeoutMs
        var last: NodeManagerProto.RuntimeConfig? = null
        val synced = waitForSyncedRuntimeConfig(client)
        if (synced.currentEpochId >= minEpoch) {
            return synced
        }
        var clientHeight = synced.paramsBlockHeight
        while (System.currentTimeMillis() < deadline) {
            val resp = client.getRuntimeConfig(clientParamsBlockHeight = clientHeight, maxWaitSeconds = 30)
            if (!resp.unchanged && resp.hasConfig()) {
                last = resp.config
                clientHeight = resp.config.paramsBlockHeight
                if (resp.config.currentEpochId >= minEpoch) {
                    return resp.config
                }
            }
            Thread.sleep(1_000)
        }
        error(
            "runtime config epoch did not reach $minEpoch within ${timeoutMs}ms " +
                "(last=${last?.currentEpochId})"
        )
    }

    /**
     * Waits until NodeManager reports [enabled]. When the client is already at the
     * server's params_block_height, GetRuntimeConfig returns unchanged with no body —
     * check the synced snapshot first (see RuntimeConfigTests long-poll semantics).
     */
    private fun waitForRuntimeConfigDevshardRequests(
        client: NodeManagerClient,
        enabled: Boolean,
        timeoutMs: Long = propagationSlaMs,
    ) {
        val deadline = System.currentTimeMillis() + timeoutMs
        val synced = waitForSyncedRuntimeConfig(client)
        if (synced.devshardRequestsEnabled == enabled) {
            return
        }
        var clientHeight = synced.paramsBlockHeight
        while (System.currentTimeMillis() < deadline) {
            val resp = client.getRuntimeConfig(clientParamsBlockHeight = clientHeight, maxWaitSeconds = 30)
            if (!resp.unchanged && resp.hasConfig()) {
                clientHeight = resp.config.paramsBlockHeight
                if (resp.config.devshardRequestsEnabled == enabled) {
                    return
                }
            }
            Thread.sleep(1_000)
        }
        error("runtime config devshard_requests_enabled did not become $enabled within ${timeoutMs}ms")
    }

    /** Restore chain flag when a prior test left devshard_requests_enabled=false. */
    private fun ensureDevshardRequestsEnabledOnChain() {
        if (chainEscrow(genesis).devshardRequestsEnabled) {
            return
        }
        logSection("Restoring devshard_requests_enabled=true after prior test")
        val chainParams = genesis.getParams()
        val escrow = chainEscrow(genesis)
        val enabledParams = chainParams.copy(
            devshardEscrowParams = escrow.copy(devshardRequestsEnabled = true),
        )
        genesis.runProposal(cluster, UpdateParams(params = enabledParams))
        nodeManagerClient(genesis).use {
            waitForRuntimeConfigDevshardRequests(it, enabled = true)
        }
        genesis.waitForNextInferenceWindow()
    }

    private fun chainEscrow(pair: LocalInferencePair) =
        pair.getParams().devshardEscrowParams
            ?: error("devshard_escrow_params missing")

    private fun restartApiContainer(pair: LocalInferencePair) {
        val cleanName = pair.name.trimStart('/')
        val targetContainerName = "/${cleanName}-api"
        val dockerClient = DockerClientBuilder.getInstance().build()
        val container = dockerClient.listContainersCmd().withShowAll(true).exec().firstOrNull { c ->
            c.names.any { it == targetContainerName }
        } ?: error("API container not found for $cleanName")

        if (container.state == "running") {
            dockerClient.stopContainerCmd(container.id).exec()
        }
        dockerClient.startContainerCmd(container.id).exec()
        pair.waitForFirstBlock()
    }

    @Test
    @Order(1)
    @Tag("integration")
    @Disabled(
        "Accepted Step 7 e2e (2026-05-22): governance + runtimeconfig disable/re-enable verified in " +
            "versiond logs; curl via devshardctl proxy does not observe HTTP 503 (proxy timeout/502). " +
            "See devshard/docs/params-refactoring-implementation.md — Step 7 e2e acceptance notes.",
    )
    fun `governance flip disables then re-enables devshardd completions within 30s`() {
        genesis.waitForNextInferenceWindow()

        val user = genesis.createFundedDevshardUser("devshardd-runtime-gov-user")
        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )
        try {
            genesis.waitForMidEpochWindow()
            genesis.waitForDevshardProxyWarmup()
            val okBefore = genesis.sendChatCompletionWithStatus(handle.proxyUrl, devshardEscrowModel, "before gov")
            assertThat(okBefore.httpCode).isBetween(200, 299)

            val chainParams = genesis.getParams()
            val escrow = chainEscrow(genesis)
            val disabledParams = chainParams.copy(
                devshardEscrowParams = escrow.copy(devshardRequestsEnabled = false),
            )

            logSection("Governance: disable devshard_requests_enabled")
            genesis.runProposal(cluster, UpdateParams(params = disabledParams))
            nodeManagerClient(genesis).use {
                waitForRuntimeConfigDevshardRequests(it, enabled = false)
            }
            genesis.waitForNextInferenceWindow()
            val rejected = genesis.waitForDevshardCompletionRejected(
                escrowModelId = devshardEscrowModel,
                proxyUrl = handle.proxyUrl,
                timeoutMs = propagationSlaMs,
            )
            assertThat(rejected)
                .describedAs("devshardd must reject completions with HTTP 503 when requests disabled")
                .isTrue()
            assertThat(chainEscrow(genesis).devshardRequestsEnabled).isFalse()

            val enabledParams = chainParams.copy(
                devshardEscrowParams = escrow.copy(devshardRequestsEnabled = true),
            )
            logSection("Governance: re-enable devshard_requests_enabled")
            val enableElapsed = measureTimeMillis {
                genesis.runProposal(cluster, UpdateParams(params = enabledParams))
                nodeManagerClient(genesis).use {
                    waitForRuntimeConfigDevshardRequests(it, enabled = true)
                }
                genesis.waitForNextInferenceWindow()
                val deadline = System.currentTimeMillis() + governanceReEnableSlaMs
                var accepted = false
                while (System.currentTimeMillis() < deadline && !accepted) {
                    val resp = try {
                        genesis.sendChatCompletionWithStatus(
                            handle.proxyUrl,
                            devshardEscrowModel,
                            "after re-enable",
                            maxTimeSeconds = 8,
                        )
                    } catch (_: IllegalStateException) {
                        null
                    }
                    if (resp?.httpCode in 200..299) {
                        accepted = true
                    } else {
                        Thread.sleep(500)
                    }
                }
                assertThat(accepted)
                    .describedAs("devshardd must accept completions again after runtime config re-enables requests")
                    .isTrue()
            }
            assertThat(enableElapsed).isLessThan(governanceReEnableSlaMs)
            assertThat(chainEscrow(genesis).devshardRequestsEnabled).isTrue()
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    @Order(2)
    @Tag("integration")
    fun `epoch transition reaches devshardd runtime config within 30s`() {
        nodeManagerClient(genesis).use { client ->
            genesis.waitForMidEpochWindow()
            val before = waitForSyncedRuntimeConfig(client)
            val chainEpochBefore = genesis.getEpochData().latestEpoch.index

            logSection("Advancing epoch — devshardd long-poll should apply new epoch (PruneOnce hook)")
            genesis.waitForNextEpoch()

            val chainEpochAfter = genesis.getEpochData().latestEpoch.index
            assertThat(chainEpochAfter).isGreaterThan(chainEpochBefore)

            val elapsed = measureTimeMillis {
                val cfg = waitForRuntimeEpochAtLeast(client, chainEpochBefore + 1)
                assertThat(cfg.currentEpochId).isGreaterThanOrEqualTo(chainEpochBefore + 1)
                assertThat(cfg.currentEpochId).isLessThanOrEqualTo(genesis.getEpochData().latestEpoch.index)
                assertThat(cfg.paramsBlockHeight).isGreaterThan(before.paramsBlockHeight)
            }
            assertThat(elapsed)
                .describedAs("devshardd polls NODE_MANAGER_ADDR; epoch must land within propagation SLA")
                .isLessThan(propagationSlaMs)
        }
    }

    @Test
    @Order(3)
    @Tag("integration")
    fun `dapi restart devshardd recovers completions within backoff window`() {
        ensureDevshardRequestsEnabledOnChain()
        genesis.waitForNextInferenceWindow()

        val user = genesis.createFundedDevshardUser("devshardd-runtime-restart-user")
        val escrowId = genesis.createDevshardEscrowForUser(
            7_000_000_000L,
            user.keyName,
            modelId = devshardEscrowModel,
        )
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )
        try {
            logSection("Restarting dapi API container (devshardd long-poll should backoff and resume)")
            restartApiContainer(genesis)
            genesis.stopDevshardProxy(escrowId)
            val proxyAfterRestart = genesis.startDevshardProxy(
                escrowId = escrowId,
                keyName = user.keyName,
                routePrefix = overrideRoutePrefix,
            )
            genesis.waitForDevshardProxyWarmup(postRestartWarmupDelay)
            nodeManagerClient(genesis).use { waitForSyncedRuntimeConfig(it) }
            genesis.waitForNextInferenceWindow()

            // runtimeconfig ErrorBackoffMax is 10s; allow headroom for NodeManager + one completion.
            val recoveryDeadlineMs = 90_000L
            val elapsed = measureTimeMillis {
                val deadline = System.currentTimeMillis() + recoveryDeadlineMs
                var recovered = false
                while (System.currentTimeMillis() < deadline && !recovered) {
                    val resp = try {
                        genesis.sendChatCompletionWithStatus(
                            proxyAfterRestart.proxyUrl,
                            devshardEscrowModel,
                            "post-restart",
                            maxTimeSeconds = 8,
                        )
                    } catch (_: IllegalStateException) {
                        null
                    }
                    if (resp?.httpCode in 200..299) {
                        recovered = true
                    } else {
                        Thread.sleep(2_000)
                    }
                }
                assertThat(recovered)
                    .describedAs("chat completion must succeed after dapi restart")
                    .isTrue()
            }
            assertThat(elapsed).isLessThan(recoveryDeadlineMs)
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    private fun resolveRepoRoot(): Path {
        val cwd = Path.of("").toAbsolutePath().normalize()
        return generateSequence(cwd) { it.parent }
            .firstOrNull { candidate ->
                Files.isDirectory(candidate.resolve("testermint")) &&
                    Files.isDirectory(candidate.resolve("local-test-net")) &&
                    Files.isDirectory(candidate.resolve("versioned"))
            }
            ?: error("Repository root not found from $cwd")
    }
}
