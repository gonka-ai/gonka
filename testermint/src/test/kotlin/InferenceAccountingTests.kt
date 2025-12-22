import com.productscience.*
import com.productscience.data.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlin.collections.component1
import kotlin.collections.component2
import kotlin.random.Random
import kotlin.test.assertNotNull

const val DELAY_SEED = 8675309

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test with maximum tokens`() {
        logSection("=== STARTING TEST: test with maximum tokens ===")
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
        val address = genesis.node.getColdAddress()
        // Phase 3: Dev signs hash of original_prompt
        val signature = genesis.node.signRequest(payload, address, timestamp, endpointAccount = address)


        CoroutineScope(Dispatchers.Default).launch {
            genesis.api.makeInferenceRequest(payload, address, signature, timestamp)
        }

        val inferenceId = signature

        var lastRequest: InferenceRequestPayload? = null
        var attempts = 0
        while (lastRequest == null && attempts < 15) {
            Thread.sleep(Duration.ofSeconds(1))
            attempts++
            lastRequest =
                cluster.allPairs.firstNotNullOfOrNull { it.mock?.getLastInferenceRequest()?.takeIf { it.seed == seed } }
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
        logSection("Clearing Claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Making inferences")
        genesis.waitForNextInferenceWindow(15)
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
        logSection("Clearing claims")
        // If we don't wait until the next rewards claim, there may be lingering requests that mess with our math
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, 3)
        genesis.waitForNextInferenceWindow()

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

    private fun getFailingInference(
        cluster: LocalCluster,
        requestingNode: LocalInferencePair = cluster.genesis,
        requester: String? = cluster.genesis.node.getColdAddress(),
        taAddress: String = requestingNode.node.getColdAddress(),
    ): List<InferencePayload> {
        var failed = false
        val results: MutableList<InferencePayload> = mutableListOf()
        while (!failed) {
            val currentBlock = cluster.genesis.getCurrentBlockHeight()
            try {
                val response = requestingNode.makeInferenceRequest(
                    inferenceRequest,
                    requester,
                    taAddress = requestingNode.node.getColdAddress()
                )
                cluster.genesis.node.waitForNextBlock(2)
                results.add(cluster.genesis.api.getInference(response.id))
            } catch (e: Exception) {
                Logger.warn(e.toString())
                var foundInference: InferencePayload? = null
                var tries = 0
                while (foundInference == null) {
                    cluster.genesis.node.waitForNextBlock(2)
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

    companion object {
        @JvmStatic
        @BeforeAll
        fun getCluster(): Unit {
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

            val (clus, gen) = initCluster(config = delayPruningConfig)
            clus.allPairs.forEach { pair ->
                pair.waitForMlNodesToLoad()
            }
            cluster = clus
            genesis = gen
        }

        lateinit var cluster: LocalCluster
        lateinit var genesis: LocalInferencePair
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
