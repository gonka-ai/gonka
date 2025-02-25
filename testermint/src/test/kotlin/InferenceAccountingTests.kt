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
import com.productscience.initCluster
import com.productscience.initialize
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import kotlin.test.assertNotNull

class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test get participants`() {
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
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val params = highestFunded.node.getInferenceParams()
        Logger.info(params)
    }

    @Test
    fun `test escrow and pre settle amounts`() {
        val (cluster, _) = initCluster()
        val inferenceResult = generateSequence {
            getInferenceResult(cluster.genesis)
        }.first { it.inference.executedBy != it.inference.requestedBy }

        val inferenceCost = inferenceResult.inference.actualCost!!

        // Escrow is too short to measure unless we have a long running request
        assertThat(inferenceResult.requesterBalanceChange).`as`("escrow withheld").isEqualTo(-inferenceCost)
        assertThat(inferenceResult.requesterOwedChange).`as`("requester not owed").isEqualTo(0)
        assertThat(inferenceResult.executorRefundChange).isEqualTo(0)
        assertThat(inferenceResult.executorBalanceChange).isEqualTo(0)
        assertThat(inferenceResult.executorOwedChange).`as`("executor owed for inference").isEqualTo(inferenceCost)
    }

    @Test
    fun `test post settle amounts`() {
        val (_, genesis) = initCluster()
        val tokenomicsAtStart = genesis.node.getTokenomics().tokenomicsData
        val participants = genesis.api.getParticipants()
        val nextSettleBlock = genesis.getNextSettleBlock()
        // If we don't wait until the next settle, there may be lingering requests that mess with our math
        genesis.node.waitForMinimumBlock(nextSettleBlock + 3)

        participants.forEach {
            Logger.info("Participant: ${it.id}, Reputation: ${it.reputation}")
        }
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis)
        }.take(4)
        val newTokens = verifySettledInferences(genesis, inferences)
        val tokenomicsAtEnd = genesis.node.getTokenomics().tokenomicsData
        assertThat(tokenomicsAtEnd.totalSubsidies).isEqualTo(
            tokenomicsAtStart.totalSubsidies +
                    newTokens.totalSubsidies
        )
        assertThat(tokenomicsAtEnd.totalFees).isEqualTo(tokenomicsAtStart.totalFees + newTokens.totalFees)

        val postParticipants = genesis.api.getParticipants()
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

    @Test
    fun `test consumer only participant`() {
        val (cluster, genesis) = initCluster()
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

    @Test
    fun createTopMiner() {
        val (_, genesis) = initCluster(reboot = true)
        genesis.mock?.setPocResponse(100)
        val nextSettle = genesis.getNextSettleBlock()
        genesis.node.waitForMinimumBlock(nextSettle + 20)
        val topMiners = genesis.node.getTopMiners()
        println(topMiners)
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(genesis.node.addresss)
        val startTime = topMiner.firstQualifiedStarted
        assertThat(topMiner.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner.lastUpdatedTime).isEqualTo(startTime)
        genesis.node.waitForMinimumBlock(nextSettle + 40)
        val topMiners2 = genesis.node.getTopMiners()
        assertThat(topMiners2.topMiner).hasSize(1)
        val topMiner2 = topMiners2.topMiner.first()
        assertThat(topMiner2.address).isEqualTo(genesis.node.addresss)
        assertThat(topMiner2.firstQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.qualifiedTime).isCloseTo(100, Offset.offset(2))
        assertThat(topMiner2.lastUpdatedTime).isEqualTo(startTime + topMiner2.qualifiedTime!!)
    }

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
        val (localCluster, genesis) = initCluster(config = fastRewards, reboot = true)
        genesis.mock?.setPocResponse(100)
        val initialBalance = genesis.node.getSelfBalance("nicoin")
        val nextSettle = genesis.getNextSettleBlock()
        genesis.node.waitForMinimumBlock(nextSettle + 40)
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(genesis.node.addresss)
        val standardizedExpectedReward = getTopMinerReward(localCluster)
        val currentBalance = genesis.node.getSelfBalance("nicoin")
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
        val (localCluster, genesis) = initCluster()
        genesis.node.waitForMinimumBlock(38)
        val nextSettle = genesis.getNextSettleBlock()
        genesis.node.waitForMinimumBlock(nextSettle + 5)
        localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
        val failingAddress = localCluster.joinPairs.first().node.getAddress()
        val inferences = getFailingInference(localCluster, failingAddress)
        val failedInference = inferences.last()
        val otherInferences = inferences.take(inferences.size - 1)

        val balanceBeforeSettle = genesis.node.getSelfBalance("nicoin")
        assertNotNull(failedInference, "Inference never finished")
        val timeouts = genesis.node.getInferenceTimeouts()
        assertThat(timeouts.inferenceTimeout).hasSizeGreaterThan(0)
        val expirationBlocks = genesis.node.getInferenceParams().validationParams.expirationBlocks + 1
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        val nextSettleBlock = genesis.getNextSettleBlock()
        genesis.node.waitForMinimumBlock(expirationBlock)
        genesis.node.waitForMinimumBlock(nextSettleBlock + 2)
        val canceledInference = localCluster.joinPairs.first().api.getInference(failedInference.index)
        assertThat(canceledInference.status).isEqualTo(InferenceStatus.EXPIRED.value)
        assertThat(canceledInference.executedBy).isNull()
        val afterTimeouts = genesis.node.getInferenceTimeouts()
        assertThat(afterTimeouts.inferenceTimeout).hasSize(0)
        val finishedInferences = otherInferences.map {
            localCluster.joinPairs.first().api.getInference(it.index)
        }
        val balanceAfterSettle = genesis.node.getSelfBalance("nicoin")
        val payouts =
            calculateBalanceChanges(finishedInferences + canceledInference, genesis.mostRecentParams!!)
        val changes = balanceAfterSettle - balanceBeforeSettle
        assertThat(changes).isEqualTo(payouts[genesis.node.addresss])
    }

    @Test
    fun `verify failed inference is refunded to consumer`() {
        val (localCluster, genesis) = initCluster()
        genesis.node.waitForMinimumBlock(38)
        localCluster.withConsumer("consumer1") { consumer ->
            val startBalance = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
            val failingAddress = localCluster.joinPairs.first().node.getAddress()
            val inferences = getFailingInference(localCluster, failingAddress, consumer.pair, consumer.address)
            val expirationBlocks = genesis.node.getInferenceParams().validationParams.expirationBlocks + 1
            val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
            genesis.node.waitForMinimumBlock(expirationBlock)
            val finishedInferences = inferences.map {
                genesis.api.getInference(it.index)
            }
            val balanceAfterSettle = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            val expectedBalance = finishedInferences.sumOf {
                if (it.status == InferenceStatus.EXPIRED.value) {
                    0
                } else {
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
