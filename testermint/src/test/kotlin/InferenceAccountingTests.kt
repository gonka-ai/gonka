import com.productscience.InferenceResult
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.data.AppState
import com.productscience.data.GenesisOnlyParams
import com.productscience.data.InferenceParams
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceState
import com.productscience.data.InferenceStatus
import com.productscience.data.Participant
import com.productscience.data.TokenomicsData
import com.productscience.data.spec
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initialize
import com.productscience.setupLocalCluster
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import kotlin.test.assertNotNull

class InferenceAccountingTests : TestermintTest() {
    @Test
    fun `test get participants`() {
        setupLocalCluster(2, inferenceConfig)
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        highestFunded.node.waitForNextBlock()
        val participants = highestFunded.api.getParticipants()
        Logger.debug(participants)
        assertThat(participants).hasSize(3)
        val nextSettleBlock = highestFunded.getNextSettleBlock()
        highestFunded.node.waitForMinimumBlock(nextSettleBlock)
        val participantsAfterEach = highestFunded.api.getParticipants()
        Logger.debug(participantsAfterEach)
    }
    @Test
    fun `test get inference params`() {
        setupLocalCluster(2, inferenceConfig)
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val params = highestFunded.node.getInferenceParams()
        Logger.info(params)
    }

    // TODO actualize and make independent run
    @Test
    fun `test escrow and pre settle amounts`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val inferenceResult = generateSequence {
            getInferenceResult(highestFunded)
        }.first { it.inference.executedBy != it.inference.requestedBy }

        val inferenceCost = inferenceResult.inference.actualCost
        val escrowHeld = inferenceResult.inference.escrowAmount!!

        assertThat(inferenceResult.requesterBalanceChange).`as`("escrow withheld").isEqualTo(-escrowHeld)
        assertThat(inferenceResult.requesterOwedChange).`as`("requester not owed").isEqualTo(0)
        assertThat(inferenceResult.requesterRefundChange).`as`("requester assigned refund")
            .isEqualTo(escrowHeld - inferenceCost!!)
        assertThat(inferenceResult.executorRefundChange).isEqualTo(0)
        assertThat(inferenceResult.executorBalanceChange).isEqualTo(0)
        assertThat(inferenceResult.executorOwedChange).`as`("executor owed for inference").isEqualTo(inferenceCost)
    }

    // TODO actualize and make independent run
    @Test
    fun `test post settle amounts`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val tokenomicsAtStart = highestFunded.node.getTokenomics().tokenomicsData
        val participants = highestFunded.api.getParticipants()
        participants.forEach {
            Logger.info("Participant: ${it.id}, Reputation: ${it.reputation}")
        }
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(highestFunded)
        }.take(4)
        val newTokens = verifySettledInferences(highestFunded, inferences)
        val tokenomicsAtEnd = highestFunded.node.getTokenomics().tokenomicsData
        val expectedTokens = tokenomicsAtStart.copy(
            totalSubsidies = tokenomicsAtStart.totalSubsidies + newTokens.totalSubsidies,
            totalFees = tokenomicsAtStart.totalFees + newTokens.totalFees,
            totalRefunded = tokenomicsAtStart.totalRefunded + newTokens.totalRefunded,
            totalBurned = tokenomicsAtStart.totalBurned + newTokens.totalBurned
        )
        assertThat(tokenomicsAtEnd).isEqualTo(expectedTokens)
        val postParticipants = highestFunded.api.getParticipants()
        postParticipants.forEach {
            Logger.info("Participant: ${it.id}, Reputation: ${it.reputation}")
        }
    }

    private fun verifySettledInferences(
        highestFunded: LocalInferencePair,
        inferences: Sequence<InferenceResult>,
    ): TokenomicsData {
        // More than just debugging, this forces the evaluation of the sequence
        Logger.info("Inference count: ${inferences.count()}")
        val preSettle = highestFunded.api.getParticipants()
        val nextSettleBlock = highestFunded.getNextSettleBlock()
        highestFunded.node.waitForMinimumBlock(nextSettleBlock + 10)

        val afterSettle = highestFunded.api.getParticipants()
        val params = highestFunded.node.getInferenceParams()
        val coinRewards = calculateCoinRewards(preSettle, params)
        var tokenomics = TokenomicsData(0, 0, 0, 0)
        // Represents the change from when we first made the inference to after the settle
        for (participant in preSettle) {
            val participantAfter = afterSettle.first { it.id == participant.id }
            assertThat(participantAfter.refundsOwed).`as`("No refunds owed after settle for ${participant.id}")
                .isEqualTo(0)
            assertThat(participantAfter.coinsOwed).`as`("No coins owed after settle for ${participant.id}").isEqualTo(0)
            val expectedTotal = participant.balance + // Balance before settle
                    participant.coinsOwed + // Coins earned for performing inferences
                    participant.refundsOwed + // refunds from excess escrow
                    coinRewards[participant]!! // coins earned from the epoch
            Logger.info(
                "Existing Balance: ${participant.balance}, Earned:${participant.coinsOwed}, " +
                        "Refunds:${participant.refundsOwed}, Rewards:${coinRewards[participant]}"
            )
            assertThat(participantAfter.balance)
                .`as`("Balance has previous coinsOwed and refundsOwed for ${participant.id}")
                .isCloseTo(expectedTotal, Offset.offset(1))
            tokenomics = tokenomics.copy(
                totalSubsidies = tokenomics.totalSubsidies + coinRewards[participant]!!,
                totalFees = tokenomics.totalFees + participant.coinsOwed,
                totalRefunded = tokenomics.totalRefunded + participant.refundsOwed
            )
        }
        return tokenomics
    }

    // TODO actualize
    @Test
    fun `test consumer only participant`() {
        val cluster = setupLocalCluster(2, inferenceConfig)
        val genesis = cluster.genesis
        cluster.withConsumer("consumer1") { consumer ->
            val balanceAtStart = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            val result = consumer.pair.makeInferenceRequest(inferenceRequest, consumer.address)
            assertThat(result).isNotNull
            var inference: InferencePayload? = null
            var tries = 0
            while (inference?.actualCost == null && tries < 5) {
                genesis.node.waitForNextBlock()
                inference = genesis.api.getInferenceOrNull(result.id)
                tries++
            }

            assertNotNull(inference, "Inference never finished")
            assertThat(inference.executedBy).isNotNull()
            assertThat(inference.requestedBy).isEqualTo(consumer.address)
            val participantsAfter = genesis.api.getParticipants()
            assertThat(participantsAfter).anyMatch { it.id == consumer.address }.`as`("Consumer listed in participants")
            val balanceAfter = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            assertThat(balanceAfter).isEqualTo(balanceAtStart - inference.actualCost!!)
                .`as`("Balance matches expectation")
        }
    }

    // TODO actualize
    @Test
    fun createTopMiner() {
        val localCluster = setupLocalCluster(2, inferenceConfig, reboot = true)
        initialize(localCluster.allPairs)
        localCluster.genesis.mock?.setPocResponse(100)
        val nextSettle = localCluster.genesis.getNextSettleBlock()
        localCluster.genesis.node.waitForMinimumBlock(nextSettle + 20)
        val topMiners = localCluster.genesis.node.getTopMiners()
        println(topMiners)
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(localCluster.genesis.node.addresss)
        val startTime = topMiner.firstQualifiedStarted
        assertThat(topMiner.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner.lastUpdatedTime).isEqualTo(startTime)
        localCluster.genesis.node.waitForMinimumBlock(nextSettle + 40)
        val topMiners2 = localCluster.genesis.node.getTopMiners()
        assertThat(topMiners2.topMiner).hasSize(1)
        val topMiner2 = topMiners2.topMiner.first()
        assertThat(topMiner2.address).isEqualTo(localCluster.genesis.node.addresss)
        assertThat(topMiner2.firstQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.qualifiedTime).isCloseTo(100, Offset.offset(2))
        assertThat(topMiner2.lastUpdatedTime).isEqualTo(startTime + topMiner2.qualifiedTime!!)
    }

    // TODO actualize
    @Test
    fun payTopMiner() {
        val fastRewardSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::topRewardPeriod] = 100L
                }
            }
        }

        val fastRewards = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastRewardSpec) ?: fastRewardSpec
        )
        val localCluster = setupLocalCluster(2, fastRewards, reboot = true)
        initialize(localCluster.allPairs)
        localCluster.genesis.mock?.setPocResponse(100)
        val initialBalance = localCluster.genesis.node.getSelfBalance("nicoin")
        val nextSettle = localCluster.genesis.getNextSettleBlock()
        localCluster.genesis.node.waitForMinimumBlock(nextSettle + 40)
        val topMiners = localCluster.genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(localCluster.genesis.node.addresss)
        val standardizedExpectedReward = getTopMinerReward(localCluster)
        val currentBalance = localCluster.genesis.node.getSelfBalance("nicoin")
        assertThat(currentBalance - initialBalance).isEqualTo(standardizedExpectedReward)
    }

    private fun getTopMinerReward(localCluster: LocalCluster): Long {
        val genesisState = localCluster.genesis.node.getGenesisState()
        val genesisParams = genesisState.appState.inference.genesisOnlyParams
        val expectedReward = genesisParams.topRewardAmount / genesisParams.topRewardPayouts
        val standardizedExpectedReward =
            genesisState.appState.bank.denomMetadata.first().convertAmount(expectedReward, genesisParams.supplyDenom)
        return standardizedExpectedReward
    }

    @Test
    fun testCoinConversion() {
        val localCluster = setupLocalCluster(2, inferenceConfig)
        initialize(localCluster.allPairs)
        println(getTopMinerReward(localCluster))
    }

    private fun getFailingInference(
        cluster: LocalCluster,
        failingAddress: String,
        requestingNode: LocalInferencePair = cluster.genesis,
        requester: String? = cluster.genesis.node.addresss,
    ): List<InferencePayload> {
        var failed = false
        val results: MutableList<InferencePayload> = mutableListOf()
        while (!failed) {
            val currentBlock = cluster.genesis.getCurrentBlockHeight()
            try {
                val response = requestingNode.makeInferenceRequest(inferenceRequest, requester)
                cluster.genesis.node.waitForNextBlock()
                results.add(cluster.genesis.api.getInference(response.id))
            } catch (e: Exception) {
                Logger.warn(e.toString())
                var foundInference: InferencePayload? = null
                var tries = 0
                while (foundInference == null) {
                    cluster.genesis.node.waitForNextBlock()
                    val inferences = cluster.genesis.node.getInferences()
                    foundInference =
                        inferences.inference
                            .firstOrNull { it.assignedTo == failingAddress && it.startBlockHeight >= currentBlock }
                    if (tries++ > 5) {
                        error("Could not find inference after block $currentBlock")
                    }
                }
                failed = true
                results.add(foundInference)
            }
        }
        return results
    }

    @Test
    fun `verify failed inference is refunded`() {
        val localCluster = setupLocalCluster(2, inferenceConfig)
        initialize(localCluster.allPairs)
        localCluster.genesis.node.waitForMinimumBlock(38)
        val nextSettle = localCluster.genesis.getNextSettleBlock()
        localCluster.genesis.node.waitForMinimumBlock(nextSettle + 5)
        localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
        val failingAddress = localCluster.joinPairs.first().node.getAddress()
        val inferences = getFailingInference(localCluster, failingAddress)
        val failedInference = inferences.last()
        val otherInferences = inferences.take(inferences.size - 1)

        val balanceBeforeSettle = localCluster.genesis.node.getSelfBalance("nicoin")
        assertNotNull(failedInference, "Inference never finished")
        val timeouts = localCluster.genesis.node.getInferenceTimeouts()
        assertThat(timeouts.inferenceTimeout).hasSizeGreaterThan(0)
        val expirationBlocks = localCluster.genesis.node.getInferenceParams().validationParams.expirationBlocks + 1
        val expirationBlock = localCluster.genesis.getCurrentBlockHeight() + expirationBlocks
        val nextSettleBlock = localCluster.genesis.getNextSettleBlock()
        localCluster.genesis.node.waitForMinimumBlock(expirationBlock)
        localCluster.genesis.node.waitForMinimumBlock(nextSettleBlock + 2)
        val canceledInference = localCluster.joinPairs.first().api.getInference(failedInference.index)
        assertThat(canceledInference.status).isEqualTo(InferenceStatus.EXPIRED.value)
        assertThat(canceledInference.executedBy).isNull()
        val afterTimeouts = localCluster.genesis.node.getInferenceTimeouts()
        assertThat(afterTimeouts.inferenceTimeout).hasSize(0)
        val finishedInferences = otherInferences.map {
            localCluster.joinPairs.first().api.getInference(it.index)
        }
        val balanceAfterSettle = localCluster.genesis.node.getSelfBalance("nicoin")
        val payouts =
            calculateBalanceChanges(finishedInferences + canceledInference, localCluster.genesis.mostRecentParams!!)
        val changes = balanceAfterSettle - balanceBeforeSettle
        assertThat(changes).isEqualTo(payouts[localCluster.genesis.node.addresss])
    }

    @Test
    fun `verify failed inference is refunded to consumer`() {
        val localCluster = setupLocalCluster(2, inferenceConfig)
        initialize(localCluster.allPairs)
        val genesis = localCluster.genesis
        localCluster.genesis.node.waitForMinimumBlock(38)
        localCluster.withConsumer("consumer1") { consumer ->
            val startBalance = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
            val failingAddress = localCluster.joinPairs.first().node.getAddress()
            val inferences = getFailingInference(localCluster, failingAddress, consumer.pair, consumer.address)
            val expirationBlocks = genesis.node.getInferenceParams().validationParams.expirationBlocks + 1
            val expirationBlock = localCluster.genesis.getCurrentBlockHeight() + expirationBlocks
            genesis.node.waitForMinimumBlock(expirationBlock)
            val finishedInferences = inferences.map {
                genesis.api.getInference(it.index)
            }
            val balanceAfterSettle = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            val expectedBalance = finishedInferences.sumOf {
                if (it.status == InferenceStatus.EXPIRED.value) {
                    0
                }
                else {
                    it.actualCost!!
                }
            }
            val changes = startBalance - balanceAfterSettle
            assertThat(changes).isEqualTo(expectedBalance)
        }
    }

    private fun calculateBalanceChanges(
        inferences: List<InferencePayload>,
        inferenceParams: InferenceParams,
    ): Map<String, Long> {
        val payouts: MutableMap<String, Long> = mutableMapOf()
        inferences.forEach { inference ->
            when (inference.status) {
                InferenceStatus.STARTED.value -> {}
                // no payouts
                InferenceStatus.FINISHED.value, InferenceStatus.VALIDATED.value -> {
                    // refund from escrow
                    payouts.add(inference.requestedBy, inference.escrowAmount!! - inference.actualCost!!)
                    payouts.add(inference.assignedTo!!, inference.actualCost)
                    payouts.add(inference.assignedTo, calculateRewards(inferenceParams, inference.actualCost))
                }

                InferenceStatus.EXPIRED.value, InferenceStatus.INVALIDATED.value -> {
                    // full refund
                    payouts.add(inference.requestedBy, inference.escrowAmount!!)
                }
            }
        }
        return payouts
    }

    private fun MutableMap<String, Long>.add(key: String, amount: Long) {
        this[key] = (this[key] ?: 0) + amount
    }

    private fun calculateCoinRewards(
        preSettle: List<Participant>,
        params: InferenceParams,
    ): Map<Participant, Long> {
        return preSettle.associateWith { participant ->
            calculateRewards(params, participant.coinsOwed)
        }
    }

    private fun calculateRewards(params: InferenceParams, earned: Long): Long {
        val bonusPercentage = params.tokenomicsParams.currentSubsidyPercentage
        val coinsForParticipant = (earned / (1 - bonusPercentage)).toLong()
        Logger.info(
            "Owed: $earned, Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant"
        )
        return coinsForParticipant
    }
}
