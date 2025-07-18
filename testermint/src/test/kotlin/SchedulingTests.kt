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

        // TODO: assert weight == 20

        val allocatedNode = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (key, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                    assertThat(value.timeslotAllocation[0]).isTrue
                    assertThat(value.timeslotAllocation[1]).isTrue
                }
            }
            nodes.firstNotNullOf { node ->
                val isAllocatedForInference = node.state.epochMlNodes
                    ?.firstNotNullOf { (_, x) -> x.timeslotAllocation.getOrNull(1) == true  }
                    ?: false
                node.takeIf { isAllocatedForInference }
            }
        }

        assertThat(allocatedNode).isNotNull

        genesis.waitForStage(EpochStage.START_OF_POC)

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (key, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                    assertThat(value.timeslotAllocation[0]).isTrue
                    assertThat(value.timeslotAllocation[1]).isTrue
                }
            }
            nodes.forEach { node ->
                if (node.node.id == allocatedNode.node.id) {
                    assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                    assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
                } else {
                    assertThat(node.state.currentStatus).isEqualTo("POC")
                    assertThat(node.state.intendedStatus).isEqualTo("POC")
                }
            }
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        // TODO: assert weight == 20
        val allocatedNode2 = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)

            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (key, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                    assertThat(value.timeslotAllocation[0]).isTrue
                    assertThat(value.timeslotAllocation[1]).isTrue
                }
            }

            nodes.forEach { node ->
                assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
            }

            nodes.firstNotNullOf { node ->
                val isAllocatedForInference = node.state.epochMlNodes
                    ?.firstNotNullOf { (_, x) -> x.timeslotAllocation.getOrNull(1) == true  }
                    ?: false
                node.takeIf { isAllocatedForInference }
            }
        }

        assertThat(allocatedNode2).isNotNull
    }
}
