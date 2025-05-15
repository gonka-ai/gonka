import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.cosmosJson
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.data.ModelConfig
import com.productscience.defaultInferenceResponseObject
import com.productscience.defaultModel
import com.productscience.getInferenceResult
import com.productscience.inferenceRequestObject
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.validNode
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.Logger

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
                host = "${it.name.trim('/')}-wiremock", pocPort = 8080, inferencePort = 8080, models = mapOf(
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
        val (newModelName, secondModelPairs) = setSecondModel(cluster, genesis)
        logSection("making inferences")
        val participants = genesis.api.getParticipants()
        val models = listOf(defaultModel, newModelName)
        val inferences = generateSequence { getInferenceResult(genesis, models.random()) }.take(5)
        verifySettledInferences(genesis, inferences, participants)
    }


}
