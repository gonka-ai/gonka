import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import kotlin.test.Test

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class SchedulingTests : TestermintTest() {
    @Test
    fun basicSchedulingTest() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName= mapOf(
                "genesis" to listOf("docker-compose-local-mock-node-2.yml")
            )
        )
        val (cluster, genesis) = initCluster(config = config, reboot = true)
        logSection("Scheduling a basic task")
    }
}
