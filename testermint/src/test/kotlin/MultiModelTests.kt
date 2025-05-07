import com.productscience.data.ModelConfig
import com.productscience.defaultInferenceResponseObject
import com.productscience.defaultModel
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.validNode
import org.junit.jupiter.api.Test
import org.tinylog.Logger

class MultiModelTests : TestermintTest() {
    @Test
    fun `simple multi model`() {
        val (cluster, genesis) = initCluster(3)
        val newModelName = "newModel-1"
        val secondModelPairs = cluster.joinPairs.take(2) + genesis

        logSection("Setting nodes for new model")
        cluster.allPairs.forEach { pair ->
            pair.api.getNodes().forEach { node ->
                Logger.info("Pair: ${pair.name} Node: ${node.node.id} Models: ${node.node.models}", "")
            }
        }

        secondModelPairs.forEach {
            val newNode = validNode.copy(
                host = "${it.name.trim('/')}-wiremock", pocPort = 8080, inferencePort = 8080, models = mapOf(
                    newModelName to ModelConfig(
                        args = emptyList()
                    ), defaultModel to ModelConfig(args = emptyList())
                )
            )
            it.api.setNodesTo(newNode)
            it.mock?.setInferenceResponse(defaultInferenceResponseObject.withResponse("Hawaii doesn't exist."), model = newModelName)
        }


        genesis.makeInferenceRequest()


    }

}
