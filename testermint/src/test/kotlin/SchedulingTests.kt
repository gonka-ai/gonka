import com.productscience.EpochStage
import com.productscience.GENESIS_KEY_NAME
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import kotlin.test.Test

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class SchedulingTests : TestermintTest() {
    @Test
    fun basicSchedulingTest() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName= mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose-local-mock-node-2.yml")
            ),
            nodeConfigFileByKeyName = mapOf(
                GENESIS_KEY_NAME to "node_payload_mock-server_genesis_2_nodes.json"
            ),
        )
        val (_, genesis) = initCluster(config = config, reboot = true, resetMlNodesToDefaultNode = false)

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        // TODO: assert weight == 20
        val nodeId = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
/*            nodes.firstOrNull { node ->
            }*/
            "x" // TODO: find the node with poc slot allocated
        }

        genesis.waitForStage(EpochStage.START_OF_POC)

        // TODO: assert weight == 20
        // TODO: check that the node is in Inference state
        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        // TODO: assert weight of participant is still == 20

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
        }
    }
}
