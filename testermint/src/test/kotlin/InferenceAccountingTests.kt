import com.productscience.*
import com.productscience.data.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import org.assertj.core.api.Assertions.assertThat
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
import java.time.Instant

const val DELAY_SEED = 8675309

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test with maximum tokens`() {
        logSection("=== STARTING TEST: test with maximum tokens ===")
        val (cluster, genesis) = initCluster()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        val maxCompletionTokens = 100
        
        // Test 1: maxCompletionTokens parameter
        logSection("=== TEST 1: Testing maxCompletionTokens = $maxCompletionTokens ===")
        val expectedTokens1 = (maxCompletionTokens + inferenceRequestObject.textLength())
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxCompletionTokens = maxCompletionTokens),
            expectedTokens1,
            maxCompletionTokens
        )
        
        logSection("=== TEST 1 COMPLETED ===")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Test 2: maxTokens parameter  
        logSection("=== TEST 2: Testing maxTokens = $maxCompletionTokens ===")
        val expectedTokens2 = (maxCompletionTokens + inferenceRequestObject.textLength())
        verifyEscrow(
            cluster,
            inferenceRequestObject.copy(maxTokens = maxCompletionTokens),
            expectedTokens2,
            maxCompletionTokens
        )
        
        logSection("=== TEST 2 COMPLETED ===")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Test 3: Default tokens
        logSection("=== TEST 3: Testing default tokens = $DEFAULT_TOKENS ===")
        val expectedTokens3 = (DEFAULT_TOKENS + inferenceRequestObject.textLength())
        verifyEscrow(
            cluster,
            inferenceRequestObject,
            expectedTokens3.toInt(),
            DEFAULT_TOKENS.toInt()
        )
        
        logSection("=== ALL TESTS COMPLETED SUCCESSFULLY ===")
    }

    private fun verifyEscrow(
        cluster: LocalCluster,
        inference: InferenceRequestPayload,
        expectedTokens: Int,
        expectedMaxTokens: Int,
    ) {
        logSection("Sending inference request")
        val genesis = cluster.genesis
        val startBalance = genesis.node.getSelfBalance()
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(20))
        }
        val seed = Random.nextInt()
        val payload = inference.copy(seed = seed).toJson()
        val timestamp = Instant.now().toEpochNanos()
        val address = genesis.node.getAddress()
        val signature = genesis.node.signPayload(payload, address, timestamp, endpointAccount = address)


        CoroutineScope(Dispatchers.Default).launch {
            genesis.api.makeInferenceRequest(payload, address, signature, timestamp)
        }

        val inferenceId = signature

        var lastRequest: InferenceRequestPayload? = null
        var attempts = 0
        while (lastRequest == null && attempts < 15) {
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

        logSection("Waiting for inference to be on chain")
        // Wait for inference to be available
        val chainInference = genesis.waitForInference(inferenceId, finished = false)
        assertNotNull(chainInference)
        // Balance verification
        val difference = (0..100).asSequence().map {
            Thread.sleep(100)
            val currentBalance = genesis.node.getSelfBalance()
            startBalance - currentBalance
        }.filter { it != 0L }.first()
        val expectedCost = expectedTokens * (chainInference.perTokenPrice ?: DEFAULT_TOKEN_COST)
        
        logHighlight("Balance verification: deducted $difference nicoin (expected: $expectedCost)")
        assertThat(difference).isEqualTo(expectedCost)
        logHighlight("✅ Escrow verification completed successfully")
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
        val delayPruningSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::inferencePruningEpochThreshold] = 4L
                    }
                }
            }
        }
        val delayPruningConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(delayPruningSpec) ?: delayPruningSpec
        )
        val (_, genesis) = initCluster(config = delayPruningConfig, reboot = true)
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
        val delayPruningSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::inferencePruningEpochThreshold] = 4L
                    }
                }
            }
        }
        val delayPruningConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(delayPruningSpec) ?: delayPruningSpec
        )
        val (_, genesis) = initCluster(config = delayPruningConfig, reboot = true)
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
            val inference = genesis.waitForInference(result.id, finished = true)
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
            genesis.node.getAddress(),
            startEpochIndex = startLastRewardedEpoch,
            currentEpochIndex = currentLastRewardedEpoch,
            failureEpoch = null
        )
        val actualChange = balanceAfterSettle - balanceAtStart
        
        logHighlight("Failed inference balance verification - Actual: $actualChange, Expected: $expectedChange")
        logHighlight("Reward calculation range - StartLastRewardedEpoch: $startLastRewardedEpoch, CurrentLastRewardedEpoch: $currentLastRewardedEpoch, RewardRange: ${startLastRewardedEpoch + 1} to $currentLastRewardedEpoch")
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
