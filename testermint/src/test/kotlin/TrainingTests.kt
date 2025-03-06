import com.productscience.data.HardwareResourcesDto
import com.productscience.data.StartTrainingDto
import com.productscience.data.TrainingConfigDto
import com.productscience.data.TrainingDatasetsDto
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.setupLocalCluster
import org.junit.jupiter.api.Test
import java.time.Duration

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

        instance.node.waitFor(
            check = { app ->
                // FIXME
                val result = app.exec(listOf("inferenced", "query", "inference", "training-task-all"))
                true
            },
            description = "Training assigned",
            timeout = Duration.ofSeconds(40),
            sleepTimeMillis = 3000
        )

        println("RESPONSE!!!")
        println(response)
    }
}
