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
import com.productscience.data.spec
import com.productscience.getInferenceResult
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initCluster
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.time.Duration
import kotlin.test.assertNotNull

class InferenceAccountingTests : TestermintTest() {

    @Test
    @Tag("health")
    fun `test immediate pre settle amounts`() {
        val (cluster, genesis) = initCluster()
        genesis.waitForNextSettle()
        val beforeBalances = genesis.api.getParticipants()
        val inferenceResult = getInferenceResult(genesis)
        val afterBalances = genesis.api.getParticipants()
        val expectedCoinBalanceChanges = expectedCoinBalanceChanges(listOf(inferenceResult.inference))
        expectedCoinBalanceChanges.forEach { (address, change) ->
            assertThat(afterBalances.first { it.id == address }.coinsOwed).isEqualTo(
                beforeBalances.first { it.id == address }.coinsOwed + change
            )
        }
    }

    fun expectedCoinBalanceChanges(inferences: List<InferencePayload>): Map<String, Long> {
        val payouts: MutableMap<String, Long> = mutableMapOf()
        inferences.forEach { inference ->
            when (inference.status) {
                InferenceStatus.STARTED.value -> {}
                // no payouts
                InferenceStatus.FINISHED.value -> {
                    require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                    require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                    payouts.add(inference.assignedTo, inference.actualCost, "Full Inference")
                }

                InferenceStatus.VALIDATED.value -> {
                    require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                    require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                    val validators = listOf(inference.assignedTo) + inference.validatedBy
                    validators.forEach { validator ->
                        payouts.add(validator, inference.actualCost / validators.size, "Validator share")
                    }
                    payouts.add(inference.assignedTo, inference.actualCost % validators.size, "Validator remainder")
                }
            }
        }
        return payouts
    }

    @Test
    @Tag("health")
    fun `test post settle amounts`() {
        val (_, genesis) = initCluster()
        val nextSettleBlock = genesis.getNextSettleBlock()
        // If we don't wait until the next settle, there may be lingering requests that mess with our math
        genesis.node.waitForMinimumBlock(nextSettleBlock + 1)
        val participants = genesis.api.getParticipants()

        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis)
        }.take(1)
        verifySettledInferences(genesis, inferences, participants)
    }

    private fun verifySettledInferences(
        highestFunded: LocalInferencePair,
        inferences: Sequence<InferenceResult>,
        beforeParticipants: List<Participant>,
    ) {
        // More than just debugging, this forces the evaluation of the sequence
        val allInferences = inferences.toList()
        val nextSettleBlock = highestFunded.getNextSettleBlock()
        highestFunded.node.waitForMinimumBlock(nextSettleBlock + 1)

        val afterSettleParticipants = highestFunded.api.getParticipants()
        afterSettleParticipants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        val afterSettleInferences = allInferences.map { highestFunded.api.getInference(it.inference.inferenceId) }
        val params = highestFunded.node.getInferenceParams().params
        val payouts = calculateBalanceChanges(afterSettleInferences, params)
        val actualChanges = beforeParticipants.associate {
            it.id to afterSettleParticipants.first { participant -> participant.id == it.id }.balance - it.balance
        }

        actualChanges.forEach { (address, change) ->
            Logger.info("BalanceChange- Participant: $address, Change: $change")
        }

        payouts.forEach { (address, change) ->
            assertThat(actualChanges[address]).`as` { "Participant $address settle change" }.isEqualTo(change)
            Logger.info("Participant: $address, Settle Change: $change")
        }
    }

    @Test
    @Tag("health")
    fun `test consumer only participant`() {
        val (cluster, genesis) = initCluster()
        genesis.waitForNextSettle()
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
        genesis.waitForNextSettle()
        genesis.mock?.setPocResponse(100)
        genesis.waitForNextSettle()
        val topMiners = genesis.node.getTopMiners()
        println(topMiners)
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(genesis.node.addresss)
        val startTime = topMiner.firstQualifiedStarted
        assertThat(topMiner.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner.lastUpdatedTime).isEqualTo(startTime)
        genesis.waitForNextSettle()
        val topMiners2 = genesis.node.getTopMiners()
        assertThat(topMiners2.topMiner).hasSize(1)
        val topMiner2 = topMiners2.topMiner.first()
        assertThat(topMiner2.address).isEqualTo(genesis.node.addresss)
        assertThat(topMiner2.firstQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.qualifiedTime).isCloseTo(50, Offset.offset(3))
        assertThat(topMiner2.lastUpdatedTime).isEqualTo(startTime + topMiner2.qualifiedTime!!)
    }

    @Test
    @Tag("health")
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
        val firstJoin = localCluster.joinPairs.first()
        firstJoin.mock?.setPocResponse(100)
        val initialBalance = firstJoin.node.getSelfBalance("nicoin")
        firstJoin.waitForNextSettle()
        val blockUntilReward = genesis.node.getGenesisState().appState.inference.genesisOnlyParams.topRewardPeriod / 5
        val settlesUntilReward = blockUntilReward / genesis.getParams().epochParams.epochLength
        (0 until settlesUntilReward+1).forEach { i ->
        // Odds of not getting either one of the requests or some of the validations are tiny
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.waitForNextSettle()
        }
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(firstJoin.node.addresss)
        val standardizedExpectedReward = getTopMinerReward(localCluster)
        val currentBalance = firstJoin.node.getSelfBalance("nicoin")
        // greater, because it's done validation work at some point, no doubt.
        assertThat(currentBalance - initialBalance).isGreaterThan(standardizedExpectedReward)
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
        genesis.waitForNextSettle()
        val balanceAtStart = genesis.node.getSelfBalance()
        localCluster.allPairs.forEach { it.mock?.setInferenceResponse("This is invalid json!!!") }
        var failure:Exception? = null
        try {
            genesis.makeInferenceRequest(inferenceRequest)
        } catch (e: Exception) {
            failure = e
        }
        assertThat(failure).isNotNull
        genesis.node.waitForNextBlock()
        val balanceBeforeSettle = genesis.node.getSelfBalance()
        val timeouts = genesis.node.getInferenceTimeouts()
        assertThat(timeouts.inferenceTimeout).hasSize(1)
        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        val nextSettleBlock = genesis.getNextSettleBlock()
        genesis.node.waitForMinimumBlock(expirationBlock)
        genesis.node.waitForMinimumBlock(nextSettleBlock + 1)
        val canceledInference = localCluster.joinPairs.first().api.getInference(timeouts.inferenceTimeout.first().inferenceId)
        assertThat(canceledInference.status).isEqualTo(InferenceStatus.EXPIRED.value)
        assertThat(canceledInference.executedBy).isNull()
        val afterTimeouts = genesis.node.getInferenceTimeouts()
        assertThat(afterTimeouts.inferenceTimeout).hasSize(0)
        val balanceAfterSettle = genesis.node.getSelfBalance()
        Logger.info("Balances: Start:$balanceAtStart BeforeSettle:$balanceBeforeSettle AfterSettle:$balanceAfterSettle")
        assertThat(balanceBeforeSettle).isEqualTo(balanceAtStart - canceledInference.escrowAmount!!)
        assertThat(balanceAfterSettle).isEqualTo(balanceAtStart)

    }

    @Test
    fun `verify failed inference is refunded to consumer`() {
        val (localCluster, genesis) = initCluster()
        genesis.waitForNextSettle()
        localCluster.withConsumer("consumer1") { consumer ->
            val startBalance = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
            val failingAddress = localCluster.joinPairs.first().node.getAddress()
            val inferences = getFailingInference(localCluster, failingAddress, consumer.pair, consumer.address)
            val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
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
                InferenceStatus.STARTED.value -> {
                    require(inference.escrowAmount != null) { "Escrow amount is null for started inference" }
                    payouts.add(inference.requestedBy, inference.escrowAmount, "initial escrow")
                }
                // no payouts
                InferenceStatus.FINISHED.value -> {
                    require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                    require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                    require(inference.escrowAmount != null) { "Escrow amount is null for finished inference" }
                    // refund from escrow
                    payouts.add(inference.requestedBy, -inference.actualCost, "actual cost")
                    payouts.add(inference.assignedTo, inference.actualCost, "full inference")
                    payouts.add(
                        inference.assignedTo,
                        calculateRewards(inferenceParams, inference.actualCost),
                        "reward for inference"
                    )
                }

                InferenceStatus.VALIDATED.value -> {
                    require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                    require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                    require(inference.validatedBy.isNotEmpty()) { "Validated by is empty for validated inference" }
                    require(inference.escrowAmount != null) { "Escrow amount is null for validated inference" }
                    val allValidators = listOf(inference.assignedTo) + inference.validatedBy
                    val workCoins = allValidators.associateWith { validator ->
                        if (validator == inference.assignedTo) {
                            payouts.add(
                                key = validator,
                                amount = inference.actualCost / allValidators.size,
                                reason = "Validation distributed work"
                            )
                            payouts.add(
                                key = validator,
                                amount = inference.actualCost % allValidators.size,
                                reason = "Validation distribution remainder"
                            )
                            inference.actualCost / allValidators.size + inference.actualCost % allValidators.size
                        } else {
                            payouts.add(
                                key = validator,
                                amount = inference.actualCost / allValidators.size,
                                reason = "Validation distributed work"
                            )
                            inference.actualCost / allValidators.size
                        }
                    }
                    workCoins.forEach { (validator, cost) ->
                        payouts.add(validator, calculateRewards(inferenceParams, cost), "reward for work")
                    }

                    // refund from escrow
                    payouts.add(inference.requestedBy, -inference.actualCost, "actual cost")
                }

                InferenceStatus.EXPIRED.value, InferenceStatus.INVALIDATED.value -> {
                    // full refund
                    payouts.add(inference.requestedBy, 0, "full refund of expired or invalidated")
                }
            }
        }
        return payouts
    }

    private fun MutableMap<String, Long>.add(key: String, amount: Long, reason: String) {
        Logger.info("$key:$amount for $reason")
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
        val coinsForParticipant = (earned / (1 - bonusPercentage.toDouble())).toLong()
        Logger.info(
            "Owed: $earned, Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant"
        )
        return coinsForParticipant
    }
}
