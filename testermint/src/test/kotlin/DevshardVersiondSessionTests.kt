import com.productscience.*
import com.productscience.data.DevshardInferenceStatus
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration

/**
 * Happy-path session lifecycle through versiond → override-forced devshardd.
 *
 * Split from DevshardStandaloneTests so CI can run this class in parallel with
 * [DevshardVersiondAdvancedTests].
 */
class DevshardVersiondSessionTests : DevshardVersiondTestBase() {

    @Test
    fun `create escrow and query devshard mempool via devshardd`() {
        val (_, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()
        waitForOverrideVersionedHealth(genesis)

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L
        val txResponse = genesis.createDevshardEscrow(escrowAmount, modelId = devshardEscrowModel)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Query devshard mempool via versioned route -- triggers lazy session creation")
        val mempool = genesis.api.getDevshardMempool(1)
        assertThat(mempool.txs).isNotNull()
        assertThat(mempool.txs).isEmpty()
    }

    @Test
    fun `create devshard escrow and query it`() {
        val (_, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()

        val creator = genesis.node.getColdAddress()
        val initialBalance = genesis.getBalance(creator)

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L
        val txResponse = genesis.createDevshardEscrow(escrowAmount, modelId = devshardEscrowModel)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Querying devshard escrow")
        val escrowResponse = genesis.node.queryDevshardEscrow(1)
        assertThat(escrowResponse.found).isTrue()
        assertThat(escrowResponse.escrow).isNotNull()
        assertThat(escrowResponse.escrow!!.creator).isEqualTo(creator)
        assertThat(escrowResponse.escrow!!.amount).isEqualTo(escrowAmount.toString())
        assertThat(escrowResponse.escrow!!.slots).hasSize(16)
        assertThat(escrowResponse.escrow!!.settled).isFalse()

        logSection("Verifying balance decreased")
        val balanceAfter = genesis.getBalance(creator)
        assertThat(balanceAfter).isEqualTo(initialBalance - escrowAmount)
    }

    @Test
    fun `devshard inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()
        waitForOverrideVersionedHealth(genesis)

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshardd-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = false,
            )
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `devshard streaming inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = streamingLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()
        waitForOverrideVersionedHealth(genesis)

        cluster.stubDevshardChatResponse(content = "hello from stream", streamDelay = Duration.ofMillis(50))

        val user = genesis.createFundedDevshardUser("devshardd-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

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

            genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = false,
            )

            logSection("Verifying inference statuses")
            val finished = genesis.getDevshardProxyInferences(handle.proxyUrl)
                .values.count { it.status == DevshardInferenceStatus.FINISHED }
            assertThat(finished)
                .describedAs("finished devshardd inferences")
                .isGreaterThanOrEqualTo(numInferences.toInt())
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }
}
