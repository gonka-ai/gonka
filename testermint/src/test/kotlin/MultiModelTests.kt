import com.productscience.*
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.data.ModelConfig
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.Logger
import java.time.Duration

class MultiModelTests : TestermintTest() {
    @Test
    fun `simple multi model`() {
        val (cluster, genesis) = initCluster(3)
        val (newModelName, secondModelPairs) = setSecondModel(cluster, genesis)
        logSection("Checking for nodes being updated")
        secondModelPairs.forEach {
            it.api.getNodes().forEach {
                Logger.info("Node: ${it.node.id} has model: ${it.node.models}", "")
            }
        }
        logSection("Making inference request")
        val differentModelRequest = cosmosJson.toJson(inferenceRequestObject.copy(model = newModelName))
        val response = genesis.makeInferenceRequest(differentModelRequest)
        assertThat(response.choices.first().message.content).isEqualTo("Hawaii doesn't exist.")
    }

    private fun setSecondModel(
        cluster: LocalCluster,
        genesis: LocalInferencePair,
        newModelName: String = "Qwen/Qwen2.5-7B-Instruct",
        joinModels: Int = 2,
    ): Pair<String, List<LocalInferencePair>> {
        val secondModelPairs = cluster.joinPairs.take(joinModels) + genesis

        logSection("Setting nodes for new model")
        secondModelPairs.forEach {
            val newNode = validNode.copy(
                host = "${it.name.trim('/')}-mock-server", pocPort = 8080, inferencePort = 8080, models = mapOf(
                    newModelName to ModelConfig(
                        args = emptyList()
                    ), defaultModel to ModelConfig(args = emptyList())
                )
            )
            it.api.setNodesTo(newNode)
            it.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse("Hawaii doesn't exist."),
                model = newModelName
            )
        }
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        return Pair(newModelName, secondModelPairs)
    }

    @Test
    @Tag("unstable")
    fun `invalidate invalid multi model response`() {
        val (cluster, genesis) = initCluster(3)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        var tries = 5
        val (newModelName, secondModelPairs) = setSecondModel(cluster, genesis)
        logSection("Setting up invalid inference")
        val oddPair = secondModelPairs.last()
        val badResponse = defaultInferenceResponseObject.withMissingLogit()
        oddPair.mock?.setInferenceResponse(badResponse, model = newModelName)
        logSection("Getting invalid inference")
        var newState: InferencePayload
        do {
            logSection("Trying to get invalid inference. Tries left: $tries")
            newState = getInferenceValidationState(genesis, oddPair, newModelName)
        } while (newState.statusEnum != InferenceStatus.INVALIDATED && tries-- > 0)
        logSection("Verifying invalidation")
        assertThat(newState.statusEnum).isEqualTo(InferenceStatus.INVALIDATED)
    }


    @Test
    fun `multi model inferences get validated and claimed`() {
        val (cluster, genesis) = initCluster(3)
        logSection("Setting up second model")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val (newModelName, secondModelPairs) = setSecondModel(cluster, genesis)
        genesis.waitForNextInferenceWindow()
        
        logSection("Getting initial participant states")
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        val beforeParticipants = genesis.api.getParticipants()
        beforeParticipants.forEach {
            logSection("Participant before: ${it.id} Balance: ${it.balance}")
        }
        
        logSection("making inferences")
        val models = listOf(defaultModel, newModelName)
        val inferences = runParallelInferencesWithResults(genesis, 30, models = models, maxConcurrentRequests = 30)
        logSection("Completed ${inferences.size} inferences")
        
        logSection("Waiting for settlement and claims")
        // We don't need to calculate exact amounts, just that the rewards goes through (claim isn't rejected)
        // genesis.waitForStage(EpochStage.START_OF_POC) // TODO: Can be deleted if works
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        logSection("Verifying balance changes")
        val afterParticipants = genesis.api.getParticipants()
        // Capture end epoch at same time as afterParticipants to measure same time period
        val endLastRewardedEpoch = getRewardCalculationEpochIndex(genesis)
        afterParticipants.forEach {
            logSection("Participant after: ${it.id} Balance: ${it.balance}")
        }
        
        // Get final inference states after settlement
        val settledInferences = inferences.map { genesis.api.getInference(it.inferenceId) }
        val params = genesis.node.getInferenceParams().params
        
        // Calculate expected balance changes using the dual reward system logic
        val expectedChanges = calculateBalanceChanges(settledInferences, params, beforeParticipants, startLastRewardedEpoch, endLastRewardedEpoch)
        val actualChanges = beforeParticipants.associate {
            it.id to afterParticipants.first { participant -> participant.id == it.id }.balance - it.balance
        }
        
        logSection("Comparing expected vs actual balance changes")
        expectedChanges.forEach { (participantId, expectedChange) ->
            val actualChange = actualChanges[participantId] ?: 0L
            logSection("Participant $participantId - Expected: $expectedChange Actual: $actualChange")
            
            // Verify that the actual change matches our calculated expectation (with small tolerance for rounding)
            assertThat(actualChange).`as`("Participant $participantId balance change")
                .isCloseTo(expectedChange, Offset.offset(3))
        }
    }
}
