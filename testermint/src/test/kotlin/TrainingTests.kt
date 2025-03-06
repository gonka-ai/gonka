import com.productscience.data.HardwareResourcesDto
import com.productscience.data.StartTrainingDto
import com.productscience.data.TrainingConfigDto
import com.productscience.data.TrainingDatasetsDto
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.setupLocalCluster
import org.junit.jupiter.api.Test

class TrainingTests : TestermintTest() {
    @Test
    fun test() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val instance = pairs[0]
        val result = instance.node.exec(listOf("inferenced", "query", "inference", "hardware-nodes-all"))
        println("NODES!!!")
        println(result)

        val response = instance.api.startTrainingTask(
            StartTrainingDto(
                listOf(
                    HardwareResourcesDto("v5e", 2u),
                    HardwareResourcesDto("A600", 50u),
                ),
                TrainingConfigDto(
                    TrainingDatasetsDto("train", "test"),
                    100u,
                )
            )
        )

        println("RESPONSE!!!")
        println(response)
    }
}
