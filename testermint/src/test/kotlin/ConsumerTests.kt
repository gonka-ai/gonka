import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import kotlin.test.assertNotNull

class ConsumerTests : TestermintTest() {
    @Test
    fun `verify failed inference is refunded to consumer`() {
        val (localCluster, genesis) = initCluster(config = consumerConfig)
        localCluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        localCluster.withConsumer("consumer1") { consumer ->
            logSection("Transferring money to consumer")
            genesis.submitTransaction(
                listOf(
                    "bank",
                    "send",
                    genesis.node.getColdAddress(),
                    consumer.address,
                    "100000" + consumerConfig.denom
                )
            )
            genesis.node.waitForNextBlock(2)
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
    fun `test consumer only participant`() {
        val (localCluster, localGenesis) = initCluster(config = consumerConfig)
        localCluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Clearing claims")
        localGenesis.waitForStage(EpochStage.CLAIM_REWARDS)
        localCluster.withConsumer("consumer1") { consumer ->
            logSection("Transferring money to consumer")
            localGenesis.submitTransaction(
                listOf(
                    "bank",
                    "send",
                    localGenesis.node.getColdAddress(),
                    consumer.address,
                    "100000" + consumerConfig.denom
                )

            )
            localGenesis.node.waitForNextBlock(2) // wait for balance to process
            val balanceAtStart = localGenesis.node.getBalance(consumer.address, "ngonka").balance.amount
            logSection("Making inference with consumer account")
            val result = consumer.pair.makeInferenceRequest(
                inferenceRequest,
                consumer.address,
                taAddress = localGenesis.node.getColdAddress()
            )
            assertThat(result).isNotNull
            logSection("Waiting for inference to finish")
            val inference = localGenesis.waitForInference(result.id, finished = true)
            assertNotNull(inference, "Inference never started")
            assertNotNull(inference.actualCost, "Inference never finished")
            logSection("Verifying inference balances")
            assertThat(inference.executedBy).isNotNull()
            assertThat(inference.requestedBy).isEqualTo(consumer.address)
            val participantsAfter = localGenesis.api.getParticipants()
            assertThat(participantsAfter).anyMatch { it.id == consumer.address }.`as`("Consumer listed in participants")
            val balanceAfter = localGenesis.node.getBalance(consumer.address, "ngonka").balance.amount
            assertThat(balanceAfter).isEqualTo(balanceAtStart - inference.actualCost!!)
                .`as`("Balance matches expectation")
        }
    }

    val consumerSpec = spec {
        // Be able to send money
        this[AppState::restrictions] = spec<RestrictionsState> {
            this[RestrictionsState::params] = spec<RestrictionsParams> {
                this[RestrictionsParams::restrictionEndBlock] = 1L // Short deadline for testing (1 blocks)
                this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>() // Start with no exemptions
                this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>() // Start with no usage tracking
            }
        }
        // Slow pruning
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::inferencePruningEpochThreshold] = 4L
                    this[EpochParams::inferencePruningEpochThreshold] = 10000L
                }
                this[InferenceParams::dynamicPricingParams] = spec<DynamicPricingParams> {
                    // Low token price for testing
                    this[DynamicPricingParams::gracePeriodEndEpoch] = 1000L
                    this[DynamicPricingParams::gracePeriodPerTokenPrice] = 1L
                }
            }
        }
    }
    val consumerConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(consumerSpec) ?: consumerSpec,
    )


}