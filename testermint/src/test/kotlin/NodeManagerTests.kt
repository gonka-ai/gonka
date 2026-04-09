import com.productscience.NodeManagerClient
import com.productscience.data.InferenceNode
import com.productscience.data.ModelConfig
import com.productscience.defaultModel
import com.productscience.initCluster
import com.productscience.nodemanager.NodeManagerProto
import io.grpc.StatusRuntimeException
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test

class NodeManagerTests : TestermintTest() {

    private fun nodeManagerClient(pair: com.productscience.LocalInferencePair): NodeManagerClient {
        val port = pair.nodeManagerGrpcHostPort
            ?: error("NodeManager gRPC port not available for ${pair.name}")
        return NodeManagerClient("localhost", port)
    }

    private fun waitForInferenceState(pair: com.productscience.LocalInferencePair) {
        repeat(20) { attempt ->
            val nodes = pair.api.getNodes()
            if (nodes.isNotEmpty() && nodes.all { it.state.currentStatus == "INFERENCE" }) return
            Thread.sleep(5_000)
            if (attempt == 19) error("Nodes never reached INFERENCE state: ${pair.api.getNodes()}")
        }
    }

    @Test
    @Tag("sanity")
    fun `acquire and release node over gRPC`() {
        val (_, genesis) = initCluster()
        waitForInferenceState(genesis)

        nodeManagerClient(genesis).use { client ->
            val acquireResp = client.acquireMLNode(defaultModel)
            assertThat(acquireResp.lockId).isNotEmpty()
            assertThat(acquireResp.endpoint).isNotEmpty()

            client.releaseMLNode(acquireResp.lockId, NodeManagerProto.ReleaseOutcome.SUCCESS)
        }
    }

    @Test
    fun `acquire returns endpoint for registered node`() {
        val (_, genesis) = initCluster()
        waitForInferenceState(genesis)

        nodeManagerClient(genesis).use { client ->
            val resp = client.acquireMLNode(defaultModel)
            assertThat(resp.lockId).isNotEmpty()
            assertThat(resp.endpoint).startsWith("http")
            client.releaseMLNode(resp.lockId, NodeManagerProto.ReleaseOutcome.SUCCESS)
        }
    }

    @Test
    fun `release with unknown lock id returns not found`() {
        val (_, genesis) = initCluster()

        nodeManagerClient(genesis).use { client ->
            assertThatThrownBy {
                client.releaseMLNode("00000000-0000-0000-0000-000000000000")
            }.isInstanceOf(StatusRuntimeException::class.java)
                .hasMessageContaining("NOT_FOUND")
        }
    }

    @Test
    fun `node can be acquired again after release`() {
        val (_, genesis) = initCluster()
        waitForInferenceState(genesis)

        nodeManagerClient(genesis).use { client ->
            val first = client.acquireMLNode(defaultModel)
            client.releaseMLNode(first.lockId)

            var second: NodeManagerProto.AcquireMLNodeResponse? = null
            run {
                repeat(20) { attempt ->
                    try {
                        second = client.acquireMLNode(defaultModel)
                        return@run
                    } catch (_: StatusRuntimeException) {
                        if (attempt == 19) error("Node was not re-acquirable after release")
                        Thread.sleep(100)
                    }
                }
            }
            assertThat(second!!.lockId).isNotEqualTo(first.lockId)
            client.releaseMLNode(second!!.lockId)
        }
    }
}
