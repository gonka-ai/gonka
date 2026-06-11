import com.productscience.*
import com.productscience.data.DevshardInferencePayload
import com.productscience.data.DevshardInferenceStatus
import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.core.DockerClientBuilder
import com.github.dockerjava.api.model.Frame
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import java.util.concurrent.Executors
import kotlin.test.assertNotNull

class DevshardTests : TestermintTest() {
    private val devshardEscrowModel = defaultModel

    private val noRestrictionsConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(devshardNoRestrictionsSpec) ?: devshardNoRestrictionsSpec
    )

    private val streamingLongEpochConfig = inferenceConfig.copy(
        genesisSpec = createSpec(
            epochLength = 20,
            epochShift = 10,
        ).merge(devshardNoRestrictionsSpec),
    )

    private val parallelLongEpochConfig = inferenceConfig.copy(
        genesisSpec = createSpec(
            epochLength = 25,
            epochShift = 10,
        ).merge(devshardNoRestrictionsSpec),
    )

    private val noRestrictionsAlwaysValidateConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec
            ?.merge(devshardNoRestrictionsSpec)
            ?.merge(devshardAlwaysValidateSpec)
            ?.merge(devshardEscrowAlwaysValidateSpec)
            ?: devshardNoRestrictionsSpec
                .merge(devshardAlwaysValidateSpec)
                .merge(devshardEscrowAlwaysValidateSpec)
    )

    private val shortSealGraceConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec
            ?.merge(devshardNoRestrictionsSpec)
            ?.merge(devshardShortSealGraceSpec)
            ?: devshardNoRestrictionsSpec.merge(devshardShortSealGraceSpec),
    )

    @Test
    fun `devshard gateway auto-seals inferences after grace timeout`() {
        val slots = devshardAutoSealGroupSize.toInt()
        val firstBatch = slots * 2

        val (cluster, genesis) = initCluster(config = shortSealGraceConfig, reboot = true)
        genesis.waitForNextEpoch()
        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshard-autoseal-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy for auto-seal test")
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName)

        try {
            genesis.waitForDevshardProxyWarmup()

            val status = genesis.getDevshardProxyStatus(handle.proxyUrl)
            assertThat(status.config.inferenceSealGraceNonces).isEqualTo(devshardAutoSealInferenceSealGraceNonces.toInt())
            assertThat(status.config.inferenceSealGraceSeconds)
                .isEqualTo(devshardAutoSealInferenceSealGraceSeconds.toInt())
            assertThat(status.config.validationRate).isEqualTo(0)

            logSection("Sending first batch ($firstBatch finished inferences)")
            for (i in 0 until firstBatch) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "autoseal batch1 $i")
                assertThat(response).isNotEmpty()
            }
            genesis.waitForFinishedDevshardInferences(handle.proxyUrl, firstBatch)

            val debugBeforeGrace = genesis.getDevshardProxyDebugState(handle.proxyUrl)
            logSection(
                "Before grace wait: live=${debugBeforeGrace.liveInferences} " +
                    "sealed=${debugBeforeGrace.sealedInferences} nonce=${debugBeforeGrace.nonce} " +
                    "live_status=${debugBeforeGrace.liveStatusCounts}",
            )
            assertThat(debugBeforeGrace.liveInferences).isGreaterThanOrEqualTo(firstBatch)
            assertThat(debugBeforeGrace.sealedInferences).isEqualTo(0)

            logSection("Waiting ${devshardAutoSealInferenceSealGraceSeconds}s inference seal grace")
            Thread.sleep((devshardAutoSealInferenceSealGraceSeconds + 2) * 1_000L)

            logSection(
                "Driving nonce to auto-seal boundary ($devshardAutoSealEveryNNonces) " +
                    "and waiting for >= $firstBatch sealed inferences",
            )
            genesis.waitForDevshardAutoSeal(
                proxyUrl = handle.proxyUrl,
                minSealed = firstBatch,
                targetNonce = devshardAutoSealEveryNNonces,
            )

            val debugAfter = genesis.getDevshardProxyDebugState(handle.proxyUrl)
            logSection(
                "After second batch: live=${debugAfter.liveInferences} " +
                    "sealed=${debugAfter.sealedInferences} nonce=${debugAfter.nonce} " +
                    "live_status=${debugAfter.liveStatusCounts}",
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
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `create devshard escrow and query it`() {
        val (cluster, genesis) = initCluster(reboot = true)

        // Wait for first epoch to complete so EffectiveEpochIndex is set.
        genesis.waitForNextEpoch()

        val creator = genesis.node.getColdAddress()
        val initialBalance = genesis.getBalance(creator)

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L  // 7 GNK
        val txResponse = genesis.createDevshardEscrow(escrowAmount, modelId = devshardEscrowModel)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Querying devshard escrow")
        val escrowResponse = genesis.node.queryDevshardEscrow(1)
        assertThat(escrowResponse.found).isTrue()
        assertThat(escrowResponse.escrow).isNotNull()
        assertThat(escrowResponse.escrow!!.creator).isEqualTo(creator)
        assertThat(escrowResponse.escrow!!.amount).isEqualTo(escrowAmount.toString())
        assertThat(escrowResponse.escrow!!.slots).hasSize(16)  // DevshardGroupSize
        assertThat(escrowResponse.escrow!!.settled).isFalse()

        logSection("Verifying balance decreased")
        val balanceAfter = genesis.getBalance(creator)
        assertThat(balanceAfter).isEqualTo(initialBalance - escrowAmount)
    }

    @Test
    fun `devshard inference e2e with settlement`() {
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshard-proxy-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy")
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName)

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.assertDevshardSettlement(handle, escrowId, user, escrowAmount, requireCompletedValidations = false)
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `devshard streaming inference e2e with settlement`() {
        val (cluster, genesis) = initCluster(config = streamingLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse(content = "hello from stream", streamDelay = Duration.ofMillis(50))

        val user = genesis.createFundedDevshardUser("devshard-proxy-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy")
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName, debugLogging = true)

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending streaming chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response =
                    genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i", stream = true)
                assertThat(response).isNotEmpty()
                assertThat(response).contains("data:")
            }

            val result = genesis.assertDevshardSettlement(handle, escrowId, user, escrowAmount, requireCompletedValidations = false)

            logSection("Verifying inference statuses")
            try {
                val finished = genesis.getDevshardProxyInferences(handle.proxyUrl)
                    .values.count { it.status == DevshardInferenceStatus.FINISHED }
                assertThat(finished)
                    .describedAs("finished devshard inferences")
                    .isGreaterThanOrEqualTo(numInferences.toInt())
            } catch (t: Throwable) {
                dumpDevshardFailureDebug(
                    genesis = genesis,
                    handle = handle,
                    escrowId = escrowId,
                    maxInferenceId = numInferences,
                    context = "streaming-status-verification",
                )
                throw t
            }
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `parallel devshard sessions with isolated settlement`() {
        val sessionCount = 6
        val (cluster, genesis) = initCluster(config = parallelLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        data class UserInfo(val keyName: String, val address: String)
        data class SessionSetup(val keyName: String, val address: String, val escrowId: Long)

        val fundAmount = 10_000_000_000L
        val escrowAmount = 7_000_000_000L

        val users = (0 until sessionCount).map { i ->
            val user = genesis.createFundedDevshardUser("devshard-proxy-parallel-$i", fundAmount)
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

        logSection("Starting $sessionCount devshard proxies")
        val handles = sessions.map { session ->
            genesis.startDevshardProxy(escrowId = session.escrowId, keyName = session.keyName, debugLogging = true)
        }

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Running $sessionCount proxy sessions in parallel")
            val dispatcher = Executors.newFixedThreadPool(sessionCount).asCoroutineDispatcher()
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }

            logSection("Syncing devshard hosts before validation observability")
            handles.forEach { handle ->
                genesis.syncDevshardProxyHosts(handle.proxyUrl)
            }

            logSection("Waiting for validation observability on active escrows")
            sessions.forEach { session ->
                genesis.waitForDevshardValidationObservability(session.escrowId, minCompleted = 1)
            }

            logSection("Finalizing, settling, and verifying $sessionCount escrows")
            sessions.zip(handles).forEach { (session, handle) ->
                try {
                    val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
                    assertThat(result.parsed.escrowId)
                        .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                        .isEqualTo(session.escrowId.toString())
                    assertThat(result.parsed.hostStats).isNotEmpty()
                    assertThat(result.parsed.signatures).isNotEmpty()
                    val obs = genesis.getDevshardShardStatsDetail(session.escrowId)
                    assertThat(obs.validationObservability.totals.completedValidations)
                        .withFailMessage("validation observability for escrow ${session.escrowId}")
                        .isGreaterThan(0)

                    val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = session.keyName)
                    assertThat(settleResp.code)
                        .withFailMessage("Settlement failed for escrow ${session.escrowId}")
                        .isEqualTo(0)

                    val escrow = genesis.node.queryDevshardEscrow(session.escrowId)
                    assertThat(escrow.escrow!!.settled)
                        .withFailMessage("Escrow ${session.escrowId} not settled")
                        .isTrue()

                    val balance = genesis.getBalance(session.address)
                    assertThat(balance)
                        .withFailMessage("User ${session.keyName} did not receive refund")
                        .isGreaterThan(fundAmount - escrowAmount)
                } catch (t: Throwable) {
                    dumpDevshardFailureDebug(
                        genesis = genesis,
                        handle = handle,
                        escrowId = session.escrowId,
                        maxInferenceId = 30,
                        context = "parallel-finalize-${session.keyName}",
                    )
                    throw t
                }
            }
        } finally {
            handles.forEach { genesis.stopDevshardProxy(it.escrowId) }
        }
    }

    @Test
    fun `create escrow and query devshard mempool`() {
        val (cluster, genesis) = initCluster(reboot = true)

        // Wait for first epoch so EffectiveEpochIndex is set.
        genesis.waitForNextEpoch()

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L  // 7 GNK
        val txResponse = genesis.createDevshardEscrow(escrowAmount, modelId = devshardEscrowModel)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Query devshard mempool -- triggers lazy session creation")
        val mempool = genesis.api.getDevshardMempool(1)
        assertThat(mempool.txs).isNotNull()
        assertThat(mempool.txs).isEmpty()
    }

    @Test
    fun `invalid inference is challenged`() {
        val (cluster, genesis) = initCluster(config = noRestrictionsAlwaysValidateConfig, reboot = true)
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

        val user = genesis.createFundedDevshardUser("devshard-proxy-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy")
        val handle = genesis.startDevshardProxy(escrowId, keyName = user.keyName, debugLogging = true)

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy (join2 mock = withMissingLogit)")
            val numInferences = 20L
            val badExecutorHostIdx = cluster.allPairs.lastIndex
            for (i in 0 until numInferences) {
                val inferenceId = i + 1L
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
                genesis.traceDevshardInferencePhase(handle, inferenceId, "after_completion")
                if (inferenceId % cluster.allPairs.size == badExecutorHostIdx.toLong()) {
                    logSection("phase-trace inference $inferenceId routed to join2 (bad mock)")
                }
            }

            genesis.waitForDevshardPreFinalize()
            logSection("Waiting for async validations before finalize")
            Thread.sleep(Duration.ofSeconds(15).toMillis())
            for (inferenceId in 1..numInferences) {
                genesis.traceDevshardInferencePhase(handle, inferenceId, "pre_finalize")
            }
            genesis.dumpDevshardChallengeTraceLogs(escrowId)

            logSection("Finalizing via proxy")
            val result = genesis.finalizeDevshardProxy(handle.proxyUrl)

            logSection("Verifying settlement data")
            assertThat(result.parsed.escrowId).isEqualTo("$escrowId")
            assertThat(result.parsed.nonce).isGreaterThan(0)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()

            logSection("Submitting settlement from user account")
            val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = user.keyName)
            assertThat(settleResp.code).isEqualTo(0)

            logSection("Verifying escrow settled")
            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            assertThat(escrow.escrow!!.settled).isTrue()

            genesis.dumpDevshardChallengeTraceLogs(escrowId)

            logSection("Verifying inference status")
            try {
                val inference = assertNotNull(genesis.findChallengedDevshardInference(handle))
                logSection("Inference: $inference")
                assertThat(inference.status).isIn(
                    DevshardInferenceStatus.CHALLENGED,
                    DevshardInferenceStatus.INVALIDATED,
                )
                assertThat(inference.votesInvalid).isNotZero()
            } catch (t: Throwable) {
                dumpDevshardFailureDebug(
                    genesis = genesis,
                    handle = handle,
                    escrowId = escrowId,
                    maxInferenceId = numInferences,
                    context = "invalid-inference-challenge-verification",
                )
                throw t
            }
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    private fun dumpDevshardFailureDebug(
        genesis: LocalInferencePair,
        handle: LocalInferencePair.DevshardProxyHandle,
        escrowId: Long,
        maxInferenceId: Long,
        context: String,
    ) {
        logSection("Debug dump start ($context, escrow=$escrowId)")

        runCatching {
            val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
            logSection("Debug finalize rawJson (escrow=$escrowId): ${result.rawJson}")
        }.onFailure { logSection("Debug finalize failed (escrow=$escrowId): ${it.message}") }

        runCatching {
            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            logSection("Debug escrow state (escrow=$escrowId): ${cosmosJson.toJson(escrow)}")
        }.onFailure { logSection("Debug escrow query failed (escrow=$escrowId): ${it.message}") }

        runCatching {
            val inferences = genesis.getDevshardProxyInferences(handle.proxyUrl)
            for (inferenceId in 1..maxInferenceId) {
                val inference = inferences[inferenceId]
                logSection("Debug inference $inferenceId (escrow=$escrowId): ${inference?.let { cosmosJson.toJson(it) } ?: "missing"}")
            }
        }.onFailure { logSection("Debug inference dump failed (escrow=$escrowId): ${it.message}") }

        runCatching {
            val grpcPort = genesis.nodeManagerGrpcHostPort
                ?: error("NodeManager gRPC port not available for ${genesis.name}")
            NodeManagerClient("localhost", grpcPort).use { client ->
                val resp = client.getRuntimeConfig(clientParamsBlockHeight = 0, maxWaitSeconds = 0)
                logSection("Debug runtime config snapshot (escrow=$escrowId): ${resp.configOrNull()?.toString()}")
            }
        }.onFailure { logSection("Debug runtime config dump failed (escrow=$escrowId): ${it.message}") }

        runCatching {
            val dockerClient = DockerClientBuilder.getInstance().build()
            listOf("genesis", "join1", "join2").forEach { name ->
                listOf("api", "proxy").forEach { svc ->
                    val containerName = "$name-$svc"
                    val collector = StringBuilder()
                    dockerClient.logContainerCmd(containerName)
                        .withStdOut(true)
                        .withStdErr(true)
                        .withTail(200)
                        .exec(
                            object : ResultCallback.Adapter<Frame>() {
                                override fun onNext(item: Frame) {
                                    collector.append(item.toString())
                                }
                            },
                        )
                        .awaitCompletion()
                    logSection("Debug docker tail for $containerName (escrow=$escrowId): $collector")
                }
            }
        }.onFailure { logSection("Debug docker tail failed (escrow=$escrowId): ${it.message}") }

        logSection("Debug dump end ($context, escrow=$escrowId)")
    }

    private fun com.productscience.nodemanager.NodeManagerProto.GetRuntimeConfigResponse.configOrNull() =
        if (hasConfig()) config else null
}
