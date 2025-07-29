import com.productscience.ChatMessage
import com.productscience.EpochStage
import com.productscience.InferenceRequestPayload
import com.productscience.InferenceResult
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.data.AppState
import com.productscience.data.Content
import com.productscience.data.InferenceParams
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceState
import com.productscience.data.InferenceStatus
import com.productscience.data.Logprobs
import com.productscience.data.Participant
import com.productscience.data.Usage
import com.productscience.data.ValidationParams
import com.productscience.data.spec
import com.productscience.defaultInferenceResponseObject
import com.productscience.expectedCoinBalanceChanges
import com.productscience.getInferenceResult
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.inferenceRequestObject
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.calculateExpectedChangeFromEpochRewards
import com.productscience.getRewardCalculationEpochIndex
import com.productscience.verifySettledInferences
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
import kotlinx.coroutines.runBlocking

const val DELAY_SEED = 8675309

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test with maximum tokens`() {
        logSection("=== STARTING TEST: test with maximum tokens ===")
        val (cluster, genesis) = initCluster()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        val maxCompletionTokens = 100
        
        // Test 1: maxCompletionTokens parameter
        logSection("=== TEST 1: Testing maxCompletionTokens = $maxCompletionTokens ===")
        val expectedCost1 = (maxCompletionTokens + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST
        logSection("Expected cost: ($maxCompletionTokens + ${inferenceRequestObject.textLength()}) × $DEFAULT_TOKEN_COST = $expectedCost1")
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxCompletionTokens = maxCompletionTokens),
            expectedCost1,
            maxCompletionTokens
        )
        
        logSection("=== TEST 1 COMPLETED ===")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Test 2: maxTokens parameter  
        logSection("=== TEST 2: Testing maxTokens = $maxCompletionTokens ===")
        val expectedCost2 = (maxCompletionTokens + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST
        logSection("Expected cost: ($maxCompletionTokens + ${inferenceRequestObject.textLength()}) × $DEFAULT_TOKEN_COST = $expectedCost2")
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxTokens = maxCompletionTokens),
            expectedCost2,
            maxCompletionTokens
        )
        
        logSection("=== TEST 2 COMPLETED ===")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Test 3: Default tokens
        logSection("=== TEST 3: Testing default tokens = $DEFAULT_TOKENS ===")
        val expectedCost3 = (DEFAULT_TOKENS + inferenceRequestObject.textLength()) * DEFAULT_TOKEN_COST
        logSection("Expected cost: ($DEFAULT_TOKENS + ${inferenceRequestObject.textLength()}) × $DEFAULT_TOKEN_COST = $expectedCost3")
        verifyEscrow(
            cluster,
            inferenceRequestObject,
            expectedCost3,
            DEFAULT_TOKENS.toInt()
        )
        
        logSection("=== ALL TESTS COMPLETED SUCCESSFULLY ===")
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

        var inferenceId: String? = null
        runBlocking {
            val response = genesis.makeInferenceRequest(inference.copy(seed = seed).toJson())
            inferenceId = response.id
        }

        var lastRequest: InferenceRequestPayload? = null
        var attempts = 0
        while (lastRequest == null && attempts < 5) {
            Thread.sleep(Duration.ofSeconds(1))
            attempts++
            lastRequest = cluster.allPairs.firstNotNullOfOrNull { it.mock?.getLastInferenceRequest()?.takeIf { it.seed == seed } }
        }

        // Mock verification
        assertThat(lastRequest).isNotNull
        assertThat(lastRequest?.maxTokens).withFailMessage { "Max tokens was not set" }.isNotNull()
        assertThat(lastRequest?.maxTokens).isEqualTo(expectedMaxTokens)
        assertThat(lastRequest?.maxCompletionTokens).withFailMessage { "Max completion tokens was not set" }.isNotNull()
        assertThat(lastRequest?.maxCompletionTokens).isEqualTo(expectedMaxTokens)
        
        // Per-token price verification  
        lastRequest?.let { request ->
            inferenceId?.let { id ->
                Thread.sleep(Duration.ofSeconds(2))
                
                try {
                    val chainInference = genesis.api.getInference(id)
                    
                    logSection("Per-token price verification: ${chainInference.perTokenPrice} (expected: $DEFAULT_TOKEN_COST)")
                    
                    assertThat(chainInference.perTokenPrice).withFailMessage {
                        "Per-token price in inference should not be null"
                    }.isNotNull()
                    
                    assertThat(chainInference.perTokenPrice).withFailMessage {
                        "Per-token price in inference (${chainInference.perTokenPrice}) should equal DEFAULT_TOKEN_COST ($DEFAULT_TOKEN_COST)"
                    }.isEqualTo(DEFAULT_TOKEN_COST)
                    
                } catch (e: Exception) {
                    logSection("⚠️ Could not verify per-token price: ${e.message}")
                }
            }
        }
        
        // Balance verification
        val difference = (0..100).asSequence().map {
            Thread.sleep(100)
            val currentBalance = genesis.node.getSelfBalance()
            startBalance - currentBalance
        }.filter { it != 0L }.first()
        
        logSection("Balance verification: deducted $difference nicoin (expected: $expectedEscrow)")
        assertThat(difference).isEqualTo(expectedEscrow)
        logSection("✅ Escrow verification completed successfully")
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
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        val participants = genesis.api.getParticipants()

        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        logSection("Making inference")
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis, seed = DELAY_SEED)
        }.take(2)
        verifySettledInferences(genesis, inferences, participants, startLastRewardedEpoch)
    }

    @Test
    @Tag("sanity")
    fun `test post settle amounts`() {
        val (_, genesis) = initCluster()
        logSection("Clearing claims")
        // If we don't wait until the next rewards claim, there may be lingering requests that mess with our math
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        val participants = genesis.api.getParticipants()

        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        logSection("Making inference")
        val inferences: Sequence<InferenceResult> = generateSequence {
            getInferenceResult(genesis)
        }.take(1)
        verifySettledInferences(genesis, inferences, participants, startLastRewardedEpoch)
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
        val (cluster, genesis) = initCluster()
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inference that will fail")
        val balanceAtStart = genesis.node.getSelfBalance()
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        logSection("Genesis test start - Balance: $balanceAtStart, Epoch: $startLastRewardedEpoch, Address: ${genesis.node.getAddress()}")
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
        assertThat(newTimeouts).hasSize(1)
        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        genesis.node.waitForMinimumBlock(expirationBlock, "inferenceExpiration")
        genesis.waitForStage(EpochStage.START_OF_POC)
        logSection("Verifying inference was expired and refunded")
        val canceledInference =
            cluster.joinPairs.first().api.getInference(newTimeouts.first().inferenceId)
        assertThat(canceledInference.status).isEqualTo(InferenceStatus.EXPIRED.value)
        assertThat(canceledInference.executedBy).isNull()
        val afterTimeouts = genesis.node.getInferenceTimeouts()
        assertThat(afterTimeouts.inferenceTimeout).hasSize(0)
        val balanceAfterSettle = genesis.node.getSelfBalance()
        val currentLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        
        Logger.info("Balances: Start:$balanceAtStart BeforeSettle:$balanceBeforeSettle AfterSettle:$balanceAfterSettle")
        logSection("Genesis test end - Balance: $balanceAfterSettle, Epoch: $currentLastRewardedEpoch")
        logSection("Epoch progression - Start: $startLastRewardedEpoch -> End: $currentLastRewardedEpoch (${currentLastRewardedEpoch - startLastRewardedEpoch} epochs elapsed)")
        assertThat(balanceBeforeSettle).isEqualTo(balanceAtStart - canceledInference.escrowAmount!!)
        
        // Calculate expected balance change due to epoch rewards in bitcoin like rewards logic
        val expectedChange = calculateExpectedChangeFromEpochRewards(
            genesis, 
            genesis.node.getAddress(),
            startEpochIndex = startLastRewardedEpoch,
            currentEpochIndex = currentLastRewardedEpoch,
            failureEpoch = null
        )
        val actualChange = balanceAfterSettle - balanceAtStart
        
        logSection("Failed inference balance verification - Actual: $actualChange, Expected: $expectedChange")
        logSection("Reward calculation range - StartLastRewardedEpoch: $startLastRewardedEpoch, CurrentLastRewardedEpoch: $currentLastRewardedEpoch, RewardRange: ${startLastRewardedEpoch + 1} to $currentLastRewardedEpoch")
        assertThat(actualChange).isEqualTo(expectedChange)

    }

    @Test
    fun `verify failed inference is refunded to consumer`() {
        val (localCluster, genesis) = initCluster()
        logSection("Waiting to clear claims")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        localCluster.withConsumer("consumer1") { consumer ->
            logSection("Making inference that will fail")
            val startBalance = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
            localCluster.joinPairs.forEach {
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
                val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
                val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
                logSection("Waiting for inference to expire")
                genesis.node.waitForMinimumBlock(expirationBlock, "inferenceExpiration")
                logSection("Verifying inference was expired and refunded")
                val balanceAfterSettle = genesis.node.getBalance(consumer.address, "nicoin").balance.amount
                // NOTE: We don't need to add epoch rewards here as genesis node fails to claim rewards due to signature error
                // if that fixed, we need to add epoch rewards here for bitcoin like rewards logic
                val changes = startBalance - balanceAfterSettle
                assertThat(changes).isZero()
            }
            assertThat(failure).isNotNull()
        }
    }
}

const val DEFAULT_TOKENS = 5_000L
const val DEFAULT_TOKEN_COST = 1_000L

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
