import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit
import kotlin.collections.component1
import kotlin.collections.component2
import kotlin.test.assertNotNull

val DELAY_SEED = 8675309
@Timeout(value = 10, unit = TimeUnit.MINUTES)
class InferenceAccountingTests : TestermintTest() {

    @Test
    @Tag("sanity")
    fun `test immediate pre settle amounts`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inference")
        val beforeBalances = genesis.api.getParticipants()
        val inferenceResult = getInferenceResult(genesis)
        logSection("Verifying inference changes")
        val afterBalances = genesis.api.getParticipants()
        val expectedCoinBalanceChanges = expectedCoinBalanceChanges(listOf(inferenceResult.inference))
        expectedCoinBalanceChanges.forEach { (address, change) ->
            assertThat(afterBalances.first { it.id == address }.coinsOwed).isEqualTo(
                beforeBalances.first { it.id == address }.coinsOwed + change
            )
        }
    }

    @Test
    fun `start comes after finish inference`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing Claims")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inferences")
        val participants = genesis.api.getParticipants()

        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        logSection("Making inference")
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis, seed = DELAY_SEED)
        }.take(2)
        verifySettledInferences(genesis, inferences, participants)

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
    @Tag("sanity")
    fun `test post settle amounts`() {
        val (_, genesis) = initCluster()
        logSection("Clearing claims")
        // If we don't wait until the next rewards claim, there may be lingering requests that mess with our math
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        val participants = genesis.api.getParticipants()

        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        logSection("Making inference")
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis)
        }.take(1)
        verifySettledInferences(genesis, inferences, participants)
    }

    @Test
    fun `test consumer only participant`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        cluster.withConsumer("consumer1") { consumer ->
            val balanceAtStart = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            logSection("Making inference with consumer account")
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
            logSection("Verifying inference balances")
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
        logSection("Setting PoC weight to 100")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.changePoc(100)
        logSection("Verifying top miner added")
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(genesis.node.getAddress())
        val startTime = topMiner.firstQualifiedStarted
        assertThat(topMiner.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner.lastUpdatedTime).isEqualTo(startTime)
        logSection("Waiting for next Epoch")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        logSection("Verifying top miner updated")
        val topMiners2 = genesis.node.getTopMiners()
        assertThat(topMiners2.topMiner).hasSize(1)
        val topMiner2 = topMiners2.topMiner.first()
        assertThat(topMiner2.address).isEqualTo(genesis.node.getAddress())
        assertThat(topMiner2.firstQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.qualifiedTime).isCloseTo(100, Offset.offset(3))
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
        val firstJoin = localCluster.joinPairs.first()
        val initialBalance = firstJoin.node.getSelfBalance("nicoin")
        logSection("Setting PoC weight to 100")
        firstJoin.changePoc(100)
        val blockUntilReward = genesis.node.getGenesisState().appState.inference.genesisOnlyParams.topRewardPeriod / 5
        val settlesUntilReward = blockUntilReward / genesis.getParams().epochParams.epochLength
        logSection("Making Inferences")
        (0 until settlesUntilReward + 1).forEach { i ->
            logSection("Making set $i of ${settlesUntilReward + 1} inferences")
            // Odds of not getting either one of the requests or some of the validations are tiny
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            logSection("Waiting for next Epoch")
            genesis.waitForStage(EpochStage.START_OF_POC)
            genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        }
        logSection("Verifying rewards")
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(firstJoin.node.getAddress())
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
        requester: String? = cluster.genesis.node.getAddress(),
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
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.END_OF_POC)
        logSection("Making inference that will fail")
        val balanceAtStart = genesis.node.getSelfBalance()
        localCluster.allPairs.forEach { it.mock?.setInferenceResponse("This is invalid json!!!") }
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
        assertThat(timeouts.inferenceTimeout).hasSize(1)
        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        genesis.node.waitForMinimumBlock(expirationBlock, "inferenceExpiration")
        genesis.waitForStage(EpochStage.START_OF_POC)
        logSection("Verifying inference was expired and refunded")
        val canceledInference =
            localCluster.joinPairs.first().api.getInference(timeouts.inferenceTimeout.first().inferenceId)
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
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        localCluster.withConsumer("consumer1") { consumer ->
            logSection("Making inference that will fail")
            val startBalance = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            localCluster.joinPairs.first().mock?.setInferenceResponse("This is invalid json!!!")
            val failingAddress = localCluster.joinPairs.first().node.getAddress()
            val inferences = getFailingInference(localCluster, failingAddress, consumer.pair, consumer.address)
            val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
            val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
            logSection("Waiting for inference to expire")
            genesis.node.waitForMinimumBlock(expirationBlock, "inferenceExpiration")
            logSection("Verifying inference was expired and refunded")
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
}

fun verifySettledInferences(
    highestFunded: LocalInferencePair,
    inferences: Sequence<InferenceResult>,
    beforeParticipants: List<Participant>,
) {
    logSection("Waiting for settlement and claims")
    // More than just debugging, this forces the evaluation of the sequence
    val allInferences = inferences.toList()
    highestFunded.waitForStage(EpochStage.START_OF_POC)
    highestFunded.waitForStage(EpochStage.CLAIM_REWARDS)

    logSection("Verifying balance changes")
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
        assertThat(actualChanges[address]).`as` { "Participant $address settle change" }
            .isCloseTo(change, Offset.offset(3))
        Logger.info("Participant: $address, Settle Change: $change")
    }
}

fun calculateBalanceChanges(
    inferences: List<InferencePayload>,
    inferenceParams: InferenceParams,
): Map<String, Long> {
    val payouts: MutableMap<String, Long> = mutableMapOf()
    inferences.forEach { inference ->
        when (inference.status) {
            InferenceStatus.STARTED.value -> {
                require(inference.escrowAmount != null) { "Escrow amount is null for started inference" }
                payouts.add(inference.requestedBy!!, inference.escrowAmount, "initial escrow")
            }
            // no payouts
            InferenceStatus.FINISHED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for finished inference" }
                // refund from escrow
                payouts.add(inference.requestedBy!!, -inference.actualCost, "actual cost")
                payouts.add(inference.assignedTo!!, inference.actualCost, "full inference")
                payouts.add(
                    inference.assignedTo!!,
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
                payouts.add(inference.requestedBy!!, -inference.actualCost, "actual cost")
            }

            InferenceStatus.EXPIRED.value, InferenceStatus.INVALIDATED.value -> {
                // full refund
                payouts.add(inference.requestedBy!!, 0, "full refund of expired or invalidated")
            }
        }
    }
    return payouts
}

fun MutableMap<String, Long>.add(key: String, amount: Long, reason: String) {
    Logger.info("$key:$amount for $reason")
    this[key] = (this[key] ?: 0) + amount
}

fun calculateCoinRewards(
    preSettle: List<Participant>,
    params: InferenceParams,
): Map<Participant, Long> {
    return preSettle.associateWith { participant ->
        calculateRewards(params, participant.coinsOwed)
    }
}

fun calculateRewards(params: InferenceParams, earned: Long): Long {
    val bonusPercentage = params.tokenomicsParams.currentSubsidyPercentage
    val coinsForParticipant = (earned / (1 - bonusPercentage.toDouble())).toLong()
    Logger.info(
        "Owed: $earned, Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant"
    )
    return coinsForParticipant
}
