import com.productscience.GENESIS_KEY_NAME
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.getRawContainers
import com.productscience.MockServerInferenceMock
import com.productscience.EpochStage
import com.productscience.defaultInferenceResponseObject
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import com.github.dockerjava.api.model.Container
import java.time.Duration
import com.productscience.runParallelInferencesWithResults

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class InferenceRetryTests : TestermintTest() {
    @Test
    fun `configure two nodes where one returns 500 on inference`() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName = mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose-local-mock-node-2.yml")
            ),
            nodeConfigFileByKeyName = mapOf(
                GENESIS_KEY_NAME to "node_payload_mock-server_genesis_2_nodes.json"
            ),
        )
        val (_, genesis) = initCluster(config = config, reboot = true, resetMlNodes = false)

        // Ensure both nodes are present
        val nodes = genesis.api.getNodes()
        assertThat(nodes).hasSize(2)

        // Drive the chain to points where state shows INFERENCE for both
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        // Both nodes should be healthy and show INFERENCE state after validators are set
        genesis.api.getNodes().forEach { node ->
            assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
            assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
        }

        // Configure PoC participation on both mocks (weight > 0)
        val mocks = getGenesisMocks(config)
        assertThat(mocks.size).isGreaterThanOrEqualTo(2)
        val mockHealthy = mocks[0]
        val mockErroring = mocks[1]

        // Set normal inference response on the healthy node
        mockHealthy.setInferenceResponse(
            openAIResponse = defaultInferenceResponseObject,
            delay = Duration.ofMillis(0),
            streamDelay = Duration.ofMillis(0),
            segment = "",
            model = null
        )
        mockHealthy.setPocResponse(weight = 10, scenarioName = "default")

        val mlNodeVersionResponse = genesis.node.getMlNodeVersion()
        val mlNodeVersion = mlNodeVersionResponse.mlnodeVersion.currentVersion
        // Set erroring behavior on the second node (HTTP 500)
        mockErroring.setInferenceErrorResponse(
            statusCode = 500,
            errorMessage = "Internal Server Error",
            errorType = "server_error",
            delay = Duration.ofMillis(0),
            streamDelay = Duration.ofMillis(0),
            segment = "/${mlNodeVersion}",
            model = null
        )
        mockErroring.setPocResponse(weight = 10, scenarioName = "default")

        // Send multiple inference requests; all should succeed even if one node errors,
        // due to retry/failover to the healthy node.
        val inferences = runParallelInferencesWithResults(
            genesis = genesis,
            count = 8,
            waitForBlocks = 10,
            maxConcurrentRequests = 4
        )
        assertThat(inferences).hasSize(8)
        inferences.forEach { inf ->
            assertThat(inf.checkComplete()).isTrue()
        }
    }
}

private fun getGenesisMocks(config: com.productscience.ApplicationConfig): List<MockServerInferenceMock> {
    val containers = getRawContainers(config)
    // Both mock servers are labeled for the same pair name "genesis": "genesis-mock-server" and "genesis-mock-server-2"
    val genesisMocks: List<Container> = containers.mocks.filter { container ->
        container.names.any { name -> name.contains("genesis-mock-server") }
    }
    return genesisMocks.mapNotNull { c ->
        val publicPort = c.ports.find { it.privatePort == 8080 }?.publicPort ?: return@mapNotNull null
        val baseUrl = "http://localhost:$publicPort"
        val name = c.names.firstOrNull() ?: "unknown-mock"
        MockServerInferenceMock(baseUrl = baseUrl, name = name)
    }
}


