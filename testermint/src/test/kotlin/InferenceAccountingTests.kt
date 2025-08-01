import com.productscience.ChatMessage
import com.productscience.EpochStage
import com.productscience.InferenceRequestPayload
import com.productscience.InferenceResult
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.createSpec
import com.productscience.data.*
import com.productscience.defaultInferenceResponseObject
import com.productscience.getInferenceResult
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.inferenceRequestObject
import com.productscience.initCluster
import com.productscience.logSection
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.util.concurrent.TimeUnit
import kotlin.collections.component1
import kotlin.collections.component2
import kotlin.random.Random
import kotlin.test.assertNotNull

const val DELAY_SEED = 8675309

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test with maximum tokens`() {
        val (cluster, genesis) = initCluster()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        val maxCompletionTokens = 100
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxCompletionTokens = maxCompletionTokens),
            (maxCompletionTokens + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST,
            maxCompletionTokens
        )
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxTokens = maxCompletionTokens),
            (maxCompletionTokens + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST,
            maxCompletionTokens
        )
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        verifyEscrow(
            cluster,
            inferenceRequestObject,
            (DEFAULT_TOKENS + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST,
            DEFAULT_TOKENS.toInt()
        )
    }

    private fun verifyEscrow(
        cluster: LocalCluster,
        inference: InferenceRequestPayload,
        expectedEscrow: Long,
        expectedMaxTokens: Int,
    ) {
        val genesis = cluster.genesis
        val startBalance = genesis.node.getSelfBalance()
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(10))
        }
        val seed = Random.nextInt()

        CoroutineScope(Dispatchers.Default).launch {
            genesis.makeInferenceRequest(inference.copy(seed = seed).toJson())
        }

        var lastRequest: InferenceRequestPayload? = null
        var attempts = 0
        while (lastRequest == null && attempts < 5) {
            Thread.sleep(Duration.ofSeconds(1))
            attempts++
            lastRequest = cluster.allPairs.firstNotNullOfOrNull { it.mock?.getLastInferenceRequest()?.takeIf { it.seed == seed } }
        }

        assertThat(lastRequest).isNotNull
        assertThat(lastRequest?.maxTokens).withFailMessage { "Max tokens was not set" }.isNotNull()
        assertThat(lastRequest?.maxTokens).isEqualTo(expectedMaxTokens)
        assertThat(lastRequest?.maxCompletionTokens).withFailMessage { "Max completion tokens was not set" }.isNotNull()
        assertThat(lastRequest?.maxCompletionTokens).isEqualTo(expectedMaxTokens)
        val difference = (0..100).asSequence().map {
            Thread.sleep(100)
            startBalance - genesis.node.getSelfBalance()
        }.filter { it != 0L }.first()
        assertThat(difference).isEqualTo(expectedEscrow)
    }

    @Test
    @Tag("sanity")
    fun `test immediate pre settle amounts`() {
        val (_, genesis) = initCluster()
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
    fun `test prompt larger than max_tokens`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(
                defaultInferenceResponseObject.copy(
                    usage = Usage(
                        completionTokens = 500,
                        promptTokens = 10000,
                        totalTokens = 10500
                    )
                )
            )
        }
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inference")
        val genesisBalanceBefore = genesis.node.getSelfBalance()
        val beforeBalances = genesis.api.getParticipants()
        val request = inferenceRequestObject.copy(messages = listOf(ChatMessage("user", generateBigPrompt(20000))))
        val inferenceResult = getInferenceResult(genesis, baseRequest = request)
        logSection("Verifying inference changes")
        val afterBalances = genesis.api.getParticipants()
        val expectedCoinBalanceChanges = expectedCoinBalanceChanges(listOf(inferenceResult.inference))
        expectedCoinBalanceChanges.forEach { (address, change) ->
            assertThat(afterBalances.first { it.id == address }.coinsOwed).isEqualTo(
                beforeBalances.first { it.id == address }.coinsOwed + change
            )
        }
        val genesisBalanceAfter = genesis.node.getSelfBalance()
        assertThat(genesisBalanceBefore - genesisBalanceAfter).isGreaterThan(1000 * 5000)
    }

    @Test
    fun `start comes after finish inference`() {
        val (_, genesis) = initCluster()
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
            val result = consumer.pair.makeInferenceRequest(inferenceRequest, consumer.address, taAddress = genesis.node.getAddress())
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

    private fun getFailingInference(
        cluster: LocalCluster,
        requestingNode: LocalInferencePair = cluster.genesis,
        requester: String? = cluster.genesis.node.getAddress(),
        taAddress: String = requestingNode.node.getAddress(),
    ): List<InferencePayload> {
        var failed = false
        val results: MutableList<InferencePayload> = mutableListOf()
        while (!failed) {
            val currentBlock = cluster.genesis.getCurrentBlockHeight()
            try {
                val response = requestingNode.makeInferenceRequest(inferenceRequest, requester, taAddress = requestingNode.node.getAddress())
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
                            .firstOrNull { it.startBlockHeight >= currentBlock }
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
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inference that will fail")
        val balanceAtStart = genesis.node.getSelfBalance()
        val timeoutsAtStart = genesis.node.getInferenceTimeouts()
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
            localCluster.joinPairs.first().api.getInference(newTimeouts.first().inferenceId)
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
                    taAddress = genesis.node.getAddress()
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
                val balanceAfterSettle = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
                val changes = startBalance - balanceAfterSettle
                assertThat(changes).isZero()
            }
            assertThat(failure).isNotNull()
        }
    }
}

const val DEFAULT_TOKENS = 5_000L
const val DEFAULT_TOKEN_COST = 1_000L

fun verifySettledInferences(
    highestFunded: LocalInferencePair,
    inferences: Sequence<InferenceResult>,
    beforeParticipants: List<Participant>,
) {
    logSection("Waiting for settlement and claims")
    // More than just debugging, this forces the evaluation of the sequence
    val allInferences = inferences.toList()
    highestFunded.waitForStage(EpochStage.START_OF_POC)
    highestFunded.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

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
                payouts.add(inference.requestedBy!!, inference.escrowAmount!!, "initial escrow")
            }
            // no payouts
            InferenceStatus.FINISHED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for finished inference" }
                // refund from escrow
                payouts.add(inference.requestedBy!!, -inference.actualCost!!, "actual cost")
                payouts.add(inference.assignedTo!!, inference.actualCost!!, "full inference")
                payouts.add(
                    inference.assignedTo!!,
                    calculateRewards(inferenceParams, inference.actualCost!!),
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
                            key = validator!!,
                            amount = inference.actualCost!! / allValidators.size,
                            reason = "Validation distributed work"
                        )
                        payouts.add(
                            key = validator,
                            amount = inference.actualCost!! % allValidators.size,
                            reason = "Validation distribution remainder"
                        )
                        inference.actualCost!! / allValidators.size + inference.actualCost!! % allValidators.size
                    } else {
                        payouts.add(
                            key = validator!!,
                            amount = inference.actualCost!! / allValidators.size,
                            reason = "Validation distributed work"
                        )
                        inference.actualCost!! / allValidators.size
                    }
                }
                workCoins.forEach { (validator, cost) ->
                    payouts.add(validator!!, calculateRewards(inferenceParams, cost), "reward for work")
                }

                // refund from escrow
                payouts.add(inference.requestedBy!!, -inference.actualCost!!, "actual cost")
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

fun calculateRewards(params: InferenceParams, earned: Long): Long {
    val bonusPercentage = params.tokenomicsParams.currentSubsidyPercentage
    val coinsForParticipant = (earned / (1 - bonusPercentage.toDouble())).toLong()
    Logger.info(
        "Owed: $earned, Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant"
    )
    return coinsForParticipant
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
                payouts.add(inference.assignedTo!!, inference.actualCost!!, "Full Inference")
            }

            InferenceStatus.VALIDATED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                val validators = listOf(inference.assignedTo) + inference.validatedBy
                validators.forEach { validator ->
                    payouts.add(validator!!, inference.actualCost!! / validators.size, "Validator share")
                }
                payouts.add(inference.assignedTo!!, inference.actualCost!! % validators.size, "Validator remainder")
            }
        }
    }
    return payouts
}

fun generateLogProbs(content: String): Logprobs {
    return Logprobs(
        content.split(" ").map { word ->
            Content(word.toByteArray().toList().map { it.toInt() }, 0.9, word, listOf())
        }
    )
}

fun generateBigPrompt(promptChars: Int): String {
    val random = Random(42)
    val chars = ('a'..'z').toList()
    val result = StringBuilder()

    while (result.length < promptChars) {
        val wordLength = random.nextInt(1, 11)
        val word = (1..wordLength)
            .map { chars[random.nextInt(chars.size)] }
            .joinToString("")
        result.append(word).append(" ")
    }

    return result.toString()
}
