import com.productscience.*
import com.productscience.data.DevshardInferencePayload
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

class DevshardTests : TestermintTest() {
    private val devshardEscrowModel = defaultModel

    private val noRestrictionsConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(devshardNoRestrictionsSpec) ?: devshardNoRestrictionsSpec
    )

    private val noRestrictionsLongEpochConfig = inferenceConfig.copy(
        genesisSpec = createSpec(
            epochLength = 40,
            epochShift = 10
        ).merge(devshardNoRestrictionsSpec)
    )

    private val noRestrictionsAlwaysValidateConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec
            ?.merge(devshardNoRestrictionsSpec)
            ?.merge(devshardAlwaysValidateSpec)
            ?: devshardNoRestrictionsSpec.merge(devshardAlwaysValidateSpec)
    )

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
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse(content = "hello from stream", streamDelay = Duration.ofMillis(50))

        val user = genesis.createFundedDevshardUser("devshard-proxy-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy")
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName)

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
            val finished = genesis.getDevshardProxyInferences(handle.proxyUrl)
                .values.count { it.status == DevshardInferenceStatus.FINISHED }
            assertThat(finished)
                .describedAs("finished devshard inferences")
                .isGreaterThanOrEqualTo(numInferences.toInt())
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `parallel devshard sessions with isolated settlement`() {
        val sessionCount = 6
        val (cluster, genesis) = initCluster(config = noRestrictionsLongEpochConfig, reboot = true)
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
            genesis.startDevshardProxy(escrowId = session.escrowId, keyName = session.keyName)
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

            logSection("Finalizing, settling, and verifying $sessionCount escrows")
            sessions.zip(handles).forEach { (session, handle) ->
                val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
                assertThat(result.parsed.escrowId)
                    .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                    .isEqualTo(session.escrowId.toString())
                assertThat(result.parsed.hostStats).isNotEmpty()
                assertThat(result.parsed.signatures).isNotEmpty()
                assertThat(result.parsed.hostStats.sumOf { it.completedValidations }).isGreaterThan(0)

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
        val handle = genesis.startDevshardProxy(escrowId, keyName = user.keyName)

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending streaming chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.waitForDevshardPreFinalize()
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

            logSection("Verifying inference status")
            val inference = assertNotNull(genesis.findChallengedDevshardInference(handle))
            logSection("Inference: $inference")
            assertThat(inference.status).isEqualTo(DevshardInferenceStatus.CHALLENGED)
            assertThat(inference.votesInvalid).isNotZero()
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }
}
