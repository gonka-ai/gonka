import com.productscience.*
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.data.ModelConfig
import org.assertj.core.api.Assertions.assertThat
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
        newModelName: String = "Qwen/QwQ-32B",
        joinModels: Int = 2,
    ): Pair<String, List<LocalInferencePair>> {
        genesis.waitForNextInferenceWindow()

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
        genesis.node.waitForNextBlock(3)
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
            genesis.waitForNextInferenceWindow(5)
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
        logSection("making inferences")
        val join1Balance = cluster.joinPairs[0].node.getSelfBalance("nicoin")
        val join2Balance = cluster.joinPairs[1].node.getSelfBalance("nicoin")
        val join3Balance = cluster.joinPairs[2].node.getSelfBalance("nicoin")
        val models = listOf(defaultModel, newModelName)
        runParallelInferences(genesis, 30, models = models)
        logSection("Verifying some rewards given")
        // We don't need to calculate exact amounts, just that the rewards goes through (claim isn't rejected)
        // genesis.waitForStage(EpochStage.START_OF_POC) // TODO: Can be deleted if works
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        // There is 99.95% chance each pair gets at least one inference (with 30 inferences)
        // If this fails, look to see if the node that doesn't increase it's balance ever got an inference
        assertThat(cluster.joinPairs[0].node.getSelfBalance("nicoin")).isGreaterThan(join1Balance)
        assertThat(cluster.joinPairs[1].node.getSelfBalance("nicoin")).isGreaterThan(join2Balance)
        assertThat(cluster.joinPairs[2].node.getSelfBalance("nicoin")).isGreaterThan(join3Balance)
//        verifySettledInferences(genesis, inferences, participants)
    }


}
