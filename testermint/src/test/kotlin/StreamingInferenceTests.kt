import com.productscience.ChatMessage
import com.productscience.EpochStage
import com.productscience.InferenceResult
import com.productscience.data.ResponseMessage
import com.productscience.data.Usage
import com.productscience.defaultInferenceResponseObject
import com.productscience.expectedCoinBalanceChanges
import com.productscience.getInterruptedStreamingInferenceResult
import com.productscience.getRewardCalculationEpochIndex
import com.productscience.getStreamingInferenceResult
import com.productscience.inferenceRequestStreamObject
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.makeInterruptedStreamingInferenceRequest
import com.productscience.verifySettledInferences
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.random.Random

@Timeout(value = 20, unit = TimeUnit.MINUTES)

class StreamingInferenceTests : TestermintTest() {
    @Test
    @Tag("sanity")
    fun `test immediate pre settle amounts for streaming`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        genesis.waitForNextInferenceWindow()
        logSection("Making streaming inference")
        val beforeBalances = genesis.api.getParticipants()
        val inferenceResult = getStreamingInferenceResult(genesis)
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
    fun `test streaming post settle amounts`() {
        val (_, genesis) = initCluster()
        logSection("Clearing claims")
        // If we don't wait until the next rewards claim, there may be lingering requests that mess with our math
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        val participants = genesis.api.getParticipants()
        participants.forEach {
            Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
        }
        genesis.waitForNextInferenceWindow()
        logSection("Making inference")
        val inferences: Sequence<InferenceResult> = generateSequence {
            getStreamingInferenceResult(genesis)
        }.take(1)
        verifySettledInferences(genesis, inferences, participants, startLastRewardedEpoch)
    }

    @Test
    fun `test interrupted streaming request payment`() {
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        genesis.waitForNextInferenceWindow()
        logSection("Making interrupted streaming inference")
        val beforeBalances = genesis.api.getParticipants()

        val inference = makeInterruptedStreamingInferenceRequest(genesis, inferenceRequestStreamObject.toJson(), 1)
        logSection("Verifying some payment was made despite interruption")
        val actualInference = genesis.api.getInference(inference.inferenceId)
        val afterBalances = genesis.api.getParticipants()

        assertThat(actualInference.executedBy).isNotNull()
        val beforeExecutor = beforeBalances.find { it.id == actualInference.assignedTo }
        val afterExecutor = afterBalances.find { it.id == actualInference.assignedTo }

        assertThat(beforeExecutor).isNotNull()
        assertThat(afterExecutor).isNotNull()

        assertThat(afterExecutor!!.coinsOwed).isGreaterThan(beforeExecutor!!.coinsOwed)
        // Cannot test actual cost, as that will vary
    }
    @Test
    @Tag("unstable")
    fun `spam interrupted streaming requests`() {
        val maxConcurrentRequests = 50
        val totalRequests = 50
        val (cluster, genesis) = initCluster()
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        genesis.waitForNextInferenceWindow()
        logSection("Making interrupted streaming inference")
        val limitedDispatcher = Executors.newFixedThreadPool(maxConcurrentRequests).asCoroutineDispatcher()

        runBlocking {
            val requests = List(totalRequests) { i ->
                async(limitedDispatcher) {
                    Logger.info("Starting request $i")
                    makeInterruptedStreamingInferenceRequest(
                        genesis,
                        inferenceRequestStreamObject.toJson(),
                        Random.nextInt(80),
                        checkStarted = false,
                        checkFinished = false,
                    )
                }
            }
            requests.awaitAll()
        }
        Thread.sleep(Duration.ofMinutes(5))
    }

    @Test
    fun `test interrupted streaming request payment verification`() {
        val (cluster, genesis) = initCluster()
        logSection("Setting responses to large")
        val content = generateBigPrompt(100000)
        val logProbs = generateLogProbs(content)
        val longChoice = defaultInferenceResponseObject.choices.first()
            .copy(message = ResponseMessage(content, role = "user", null), logprobs = logProbs)
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(
                defaultInferenceResponseObject.copy(
                    usage = Usage(
                        completionTokens = 5000,
                        promptTokens = 10000,
                        totalTokens = 10500
                    ), choices = listOf(longChoice)
                ), streamDelay = Duration.ofMillis(200)
            )
        }
        logSection("Clearing claims")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        genesis.waitForNextInferenceWindow()
        logSection("Making interrupted streaming inference")
        val beforeBalances = genesis.api.getParticipants()

        // Make an interrupted streaming inference request
        val inferenceResult = getInterruptedStreamingInferenceResult(genesis, maxLinesToRead = 2, baseObject = inferenceRequestStreamObject.copy(messages = listOf(
            ChatMessage("user", content)
        )))

        logSection("Verifying some payment was made despite interruption")
        val afterBalances = genesis.api.getParticipants()

        // Log the inference status and other details for debugging
        Logger.info("Inference status: ${inferenceResult.inference.status}")
        Logger.info("Inference actual cost: ${inferenceResult.inference.actualCost}")

        // Get the executor (assignedTo) from the inference result
        val executor = inferenceResult.inference.assignedTo

        // Check if executor is assigned
        assertThat(executor).isNotNull()
            .withFailMessage("No executor assigned to the inference")

        // Find the executor in the before and after balances
        val executorBefore = beforeBalances.find { it.id == executor }
        val executorAfter = afterBalances.find { it.id == executor }

        // Check if executor is found in participants
        assertThat(executorBefore).isNotNull()
            .withFailMessage("Executor not found in participants before inference")
        assertThat(executorAfter).isNotNull()
            .withFailMessage("Executor not found in participants after inference")

        Logger.info("Executor before coins owed: ${executorBefore!!.coinsOwed}")
        Logger.info("Executor after coins owed: ${executorAfter!!.coinsOwed}")

        assertThat(executorAfter.coinsOwed).isGreaterThan(executorBefore.coinsOwed)
            .withFailMessage("No payment was made to the executor despite partial work being done")

        // Should be at least the size of the prompt!
        assertThat(executorAfter.coinsOwed - executorBefore.coinsOwed).isGreaterThan(10000 * DEFAULT_TOKEN_COST)
    }

}