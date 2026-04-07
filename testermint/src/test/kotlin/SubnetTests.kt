import com.productscience.*
import com.productscience.data.*
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import java.util.concurrent.Executors

class SubnetTests : TestermintTest() {

    private val noRestrictionsSpec = spec<AppState> {
        this[AppState::restrictions] = spec<RestrictionsState> {
            this[RestrictionsState::params] = spec<RestrictionsParams> {
                this[RestrictionsParams::restrictionEndBlock] = 0L
                this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>()
                this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>()
            }
        }
    }

    private val noRestrictionsConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(noRestrictionsSpec) ?: noRestrictionsSpec
    )

    @Test
    fun `create subnet escrow and query it`() {
        val (cluster, genesis) = initCluster(reboot = true)

        // Wait for first epoch to complete so EffectiveEpochIndex is set.
        genesis.waitForNextEpoch()

        val creator = genesis.node.getColdAddress()
        val initialBalance = genesis.getBalance(creator)

        logSection("Creating subnet escrow")
        val escrowAmount = 7_000_000_000L  // 7 GNK
        val txResponse = genesis.createSubnetEscrow(escrowAmount)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Querying subnet escrow")
        val escrowResponse = genesis.node.querySubnetEscrow(1)
        assertThat(escrowResponse.found).isTrue()
        assertThat(escrowResponse.escrow).isNotNull()
        assertThat(escrowResponse.escrow!!.creator).isEqualTo(creator)
        assertThat(escrowResponse.escrow!!.amount).isEqualTo(escrowAmount.toString())
        assertThat(escrowResponse.escrow!!.slots).hasSize(16)  // SubnetGroupSize
        assertThat(escrowResponse.escrow!!.settled).isFalse()

        logSection("Verifying balance decreased")
        val balanceAfter = genesis.getBalance(creator)
        assertThat(balanceAfter).isEqualTo(initialBalance - escrowAmount)
    }

    @Test
    fun `subnet inference e2e with settlement`() {
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}"""
            )
        }

        logSection("Creating separate user account")
        val userKeyName = "subnet-proxy-user"
        val userKey = genesis.node.createKey(userKeyName)
        val userAddress = userKey.address
        val fundAmount = 10_000_000_000L
        val transferResp = genesis.submitTransaction(
            listOf("bank", "send", genesis.node.getColdAddress(), userAddress, "${fundAmount}${genesis.config.denom}")
        )
        assertThat(transferResp.code).isEqualTo(0)

        genesis.waitForNextInferenceWindow()

        logSection("Creating subnet escrow from user account")
        val escrowAmount = 7_000_000_000L
        val txResp = genesis.createSubnetEscrow(escrowAmount, from = userKeyName)
        assertThat(txResp.code).isEqualTo(0)

        logSection("Starting subnet proxy")
        val handle = genesis.startSubnetProxy(escrowId = 1, keyName = userKeyName)

        try {
            logSection("Sending chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            logSection("Finalizing via proxy")
            val result = genesis.finalizeSubnetProxy(handle.proxyUrl)

            logSection("Verifying settlement data")
            assertThat(result.parsed.escrowId).isEqualTo("1")
            assertThat(result.parsed.nonce).isGreaterThan(0)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()
            val totalCompletedValidations = result.parsed.hostStats.sumOf { it.completedValidations }
            assertThat(totalCompletedValidations).isGreaterThan(0)

            logSection("Submitting settlement from user account")
            val settleResp = genesis.settleSubnetEscrow(result.rawJson, from = userKeyName)
            assertThat(settleResp.code).isEqualTo(0)

            logSection("Verifying escrow settled")
            val escrow = genesis.node.querySubnetEscrow(1)
            assertThat(escrow.escrow!!.settled).isTrue()

            logSection("Verifying user got refund")
            val balanceAfter = genesis.getBalance(userAddress)
            assertThat(balanceAfter).isGreaterThan(fundAmount - escrowAmount)
        } finally {
            genesis.stopSubnetProxy(1)
        }
    }

    @Test
    fun `subnet streaming inference e2e with settlement`() {
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"hello from stream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}""",
                streamDelay = Duration.ofMillis(50)
            )
        }

        logSection("Creating separate user account")
        val userKeyName = "subnet-proxy-stream-user"
        val userKey = genesis.node.createKey(userKeyName)
        val userAddress = userKey.address
        val fundAmount = 10_000_000_000L
        val transferResp = genesis.submitTransaction(
            listOf("bank", "send", genesis.node.getColdAddress(), userAddress, "${fundAmount}${genesis.config.denom}")
        )
        assertThat(transferResp.code).isEqualTo(0)

        genesis.waitForNextInferenceWindow()

        logSection("Creating subnet escrow from user account")
        val escrowAmount = 7_000_000_000L
        val txResp = genesis.createSubnetEscrow(escrowAmount, from = userKeyName)
        assertThat(txResp.code).isEqualTo(0)

        logSection("Starting subnet proxy")
        val handle = genesis.startSubnetProxy(escrowId = 1, keyName = userKeyName)

        try {
            logSection("Sending streaming chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i", stream = true)
                assertThat(response).isNotEmpty()
                assertThat(response).contains("data:")
            }

            logSection("Finalizing via proxy")
            val result = genesis.finalizeSubnetProxy(handle.proxyUrl)

            logSection("Verifying settlement data")
            assertThat(result.parsed.escrowId).isEqualTo("1")
            assertThat(result.parsed.nonce).isGreaterThan(0)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()

            logSection("Submitting settlement from user account")
            val settleResp = genesis.settleSubnetEscrow(result.rawJson, from = userKeyName)
            assertThat(settleResp.code).isEqualTo(0)

            logSection("Verifying escrow settled")
            val escrow = genesis.node.querySubnetEscrow(1)
            assertThat(escrow.escrow!!.settled).isTrue()
        } finally {
            genesis.stopSubnetProxy(1)
        }
    }

    @Test
    fun `parallel subnet sessions with isolated settlement`() {
        val sessionCount = 10
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}"""
            )
        }

        data class UserInfo(val keyName: String, val address: String)
        data class SessionSetup(val keyName: String, val address: String, val escrowId: Long)

        val fundAmount = 10_000_000_000L
        val escrowAmount = 7_000_000_000L

        val users = (0 until sessionCount).map { i ->
            logSection("Creating and funding user $i")
            val keyName = "subnet-proxy-parallel-$i"
            val key = genesis.node.createKey(keyName)
            val transferResp = genesis.submitTransaction(
                listOf("bank", "send", genesis.node.getColdAddress(), key.address, "${fundAmount}${genesis.config.denom}")
            )
            assertThat(transferResp.code).withFailMessage("Failed to fund user $i").isEqualTo(0)
            UserInfo(keyName, key.address)
        }

        genesis.waitForNextEpoch()
        genesis.waitForNextInferenceWindow()

        val sessions = users.mapIndexed { i, user ->
            logSection("Creating escrow for user $i")
            val txResp = genesis.createSubnetEscrow(escrowAmount, from = user.keyName)
            assertThat(txResp.code).withFailMessage("Failed to create escrow for user $i").isEqualTo(0)
            val escrowId = txResp.getEscrowId()
            assertThat(escrowId).withFailMessage("No escrow_id in tx events for user $i").isNotNull()
            SessionSetup(user.keyName, user.address, escrowId!!)
        }

        logSection("Starting $sessionCount subnet proxies")
        val handles = sessions.map { session ->
            genesis.startSubnetProxy(escrowId = session.escrowId, keyName = session.keyName)
        }

        try {
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
                val result = genesis.finalizeSubnetProxy(handle.proxyUrl)
                assertThat(result.parsed.escrowId)
                    .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                    .isEqualTo(session.escrowId.toString())
                assertThat(result.parsed.hostStats).isNotEmpty()
                assertThat(result.parsed.signatures).isNotEmpty()
                assertThat(result.parsed.hostStats.sumOf { it.completedValidations }).isGreaterThan(0)

                val settleResp = genesis.settleSubnetEscrow(result.rawJson, from = session.keyName)
                assertThat(settleResp.code)
                    .withFailMessage("Settlement failed for escrow ${session.escrowId}")
                    .isEqualTo(0)

                val escrow = genesis.node.querySubnetEscrow(session.escrowId)
                assertThat(escrow.escrow!!.settled)
                    .withFailMessage("Escrow ${session.escrowId} not settled")
                    .isTrue()

                val balance = genesis.getBalance(session.address)
                assertThat(balance)
                    .withFailMessage("User ${session.keyName} did not receive refund")
                    .isGreaterThan(fundAmount - escrowAmount)
            }
        } finally {
            handles.forEach { genesis.stopSubnetProxy(it.escrowId) }
        }
    }

    @Test
    fun `create escrow and query subnet mempool`() {
        val (cluster, genesis) = initCluster(reboot = true)

        // Wait for first epoch so EffectiveEpochIndex is set.
        genesis.waitForNextEpoch()

        logSection("Creating subnet escrow")
        val escrowAmount = 7_000_000_000L  // 7 GNK
        val txResponse = genesis.createSubnetEscrow(escrowAmount)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Query subnet mempool -- triggers lazy session creation")
        val mempool = genesis.api.getSubnetMempool(1)
        assertThat(mempool.txs).isNotNull()
        assertThat(mempool.txs).isEmpty()
    }
}
