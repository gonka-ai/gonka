import com.productscience.*
import com.productscience.data.InferenceStatus
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class InferenceFailureAccountingTests : TestermintTest() {
    @Test
    fun `verify failed inference is refunded to consumer`() {
        val (localCluster, genesis) = initCluster()
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        localCluster.withConsumer("consumer1") { consumer ->
            logSection("Making inference that will fail")
            val startBalance = genesis.node.getBalance(consumer.address, "ngonka").balance.amount
            val timeoutsAtStart = genesis.node.getInferenceTimeouts()
            localCluster.allPairs.forEach {
                it.mock?.setInferenceResponse("This is invalid json!!!")
            }
            Thread.sleep(5000)
            genesis.markNeedsReboot() // Failed inferences mess with reputations!
            var failure: Exception? = null
            try {
                genesis.waitForNextInferenceWindow()
                val result = consumer.pair.makeInferenceRequest(
                    inferenceRequest,
                    consumer.address,
                    taAddress = genesis.node.getColdAddress()
                )
            } catch(e: com.github.kittinunf.fuel.core.FuelError) {
                failure = e
                genesis.node.waitForNextBlock()
                val timeouts = genesis.node.getInferenceTimeouts()
                val newTimeouts = timeouts.inferenceTimeout.filterNot { timeoutsAtStart.inferenceTimeout.contains(it) }
                assertThat(newTimeouts).hasSize(1)
                val expirationHeight = newTimeouts.first().expirationHeight.toLong()
                logSection("Waiting for inference to expire. expirationHeight = $expirationHeight")
                genesis.node.waitForMinimumBlock(expirationHeight + 1, "inferenceExpiration")
                logSection("Verifying inference was expired and refunded")
                val balanceAfterSettle = genesis.node.getBalance(consumer.address, "ngonka").balance.amount
                // NOTE: We don't need to add epoch rewards here as genesis node fails to claim rewards due to signature error
                // if that fixed, we need to add epoch rewards here for bitcoin like rewards logic
                val changes = startBalance - balanceAfterSettle
                assertThat(changes).isZero()
            }
            assertThat(failure).isNotNull()
        }
    }

    @Test
    fun `verify failed inference is refunded`() {
        val (cluster, genesis) = initCluster()
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inference that will fail")
        val balanceAtStart = genesis.node.getSelfBalance()
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        logSection("Genesis test start - Balance: $balanceAtStart, Epoch: $startLastRewardedEpoch, Address: ${genesis.node.getColdAddress()}")
        val timeoutsAtStart = genesis.node.getInferenceTimeouts()
        cluster.allPairs.forEach { it.mock?.setInferenceResponse("This is invalid json!!!") }
        var failure: Exception? = null
        try {
            genesis.makeInferenceRequest(inferenceRequest)
        } catch (e: Exception) {
            failure = e
        }
        assertThat(failure).isNotNull
        genesis.node.waitForNextBlock()
        logSection("Waiting for inference to expire")
        val balanceBeforeSettle = genesis.node.getSelfBalance()
        val timeouts = genesis.node.getInferenceTimeouts()
        val newTimeouts = timeouts.inferenceTimeout.filterNot { timeoutsAtStart.inferenceTimeout.contains(it) }
        val queryResp1 = genesis.node.exec(listOf("inferenced", "query", "inference", "list-inference"))
        Logger.info { "QUERIED ALL INFERENCES 2:\n" + queryResp1.joinToString("\n") }
        assertThat(newTimeouts).hasSize(1)
        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        Logger.info { "EXPIRATION BLOCKS: ${expirationBlocks - 1}" }
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        genesis.node.waitForMinimumBlock(expirationBlock + 1, "inferenceExpiration")
        logSection("Verifying inference was expired and refunded")
        val queryResp2 = genesis.node.exec(listOf("inferenced", "query", "inference", "list-inference"))
        Logger.info { "QUERIED ALL INFERENCES 2 (again):\n" + queryResp2.joinToString("\n") }
        val canceledInference =
            cluster.joinPairs.first().api.getInference(newTimeouts.first().inferenceId)
        assertThat(canceledInference.status).isEqualTo(InferenceStatus.EXPIRED.value)
        assertThat(canceledInference.executedBy).isNull()
        val afterTimeouts = genesis.node.getInferenceTimeouts()
        assertThat(afterTimeouts.inferenceTimeout).hasSize(0)
        val balanceAfterSettle = genesis.node.getSelfBalance()
        val currentLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)

        Logger.info("Balances: Start:$balanceAtStart BeforeSettle:$balanceBeforeSettle AfterSettle:$balanceAfterSettle")
        logHighlight("Genesis test end - Balance: $balanceAfterSettle, Epoch: $currentLastRewardedEpoch")
        logHighlight("Epoch progression - Start: $startLastRewardedEpoch -> End: $currentLastRewardedEpoch (${currentLastRewardedEpoch - startLastRewardedEpoch} epochs elapsed)")
        assertThat(balanceBeforeSettle).isEqualTo(balanceAtStart - canceledInference.escrowAmount!!)

        // Calculate expected balance change due to epoch rewards in bitcoin like rewards logic
        val expectedChange = calculateExpectedChangeFromEpochRewards(
            genesis,
            genesis.node.getColdAddress(),
            startEpochIndex = startLastRewardedEpoch,
            currentEpochIndex = currentLastRewardedEpoch,
            failureEpoch = null
        )
        val actualChange = balanceAfterSettle - balanceAtStart

        logHighlight("Failed inference balance verification - Actual: $actualChange, Expected: $expectedChange")
        logHighlight("Reward calculation range - StartLastRewardedEpoch: $startLastRewardedEpoch, CurrentLastRewardedEpoch: $currentLastRewardedEpoch, RewardRange: ${startLastRewardedEpoch + 1} to $currentLastRewardedEpoch")
        assertThat(actualChange).isEqualTo(expectedChange)

    }

}