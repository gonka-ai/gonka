import com.productscience.data.InferenceNode
import com.productscience.data.ModelConfig
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.initialize
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class NodeManagementTests : TestermintTest() {
    @Test
    fun `get nodes`() {
        val (_, genesis) = initCluster()
        val nodes = genesis.api.getNodes()
        assertThat(nodes).hasSizeGreaterThan(0)
    }

    @Test
    fun `add node`() {
        val (_, genesis) = initCluster()
        val node = genesis.api.addNode(InferenceNode(
            host = "http://localhost:8080",
            models = listOf("model1"),
            id = "node2",
            pocPort = 100,
            inferencePort = 200,
            maxConcurrent = 1
        ))
        assertThat(node).isNotNull
        val nodes = genesis.api.getNodes()
        assertThat(nodes).anyMatch { it.node.id == "node2" }
    }

    @Test
    fun `remove nodes`() {
        val (_, genesis) = initCluster()
        val node = genesis.api.addNode(InferenceNode(
            host = "http://localhost:8080",
            pocPort = 100,
            inferencePort = 200,
            models = listOf("model1"),
            id = "nodeToRemove",
            maxConcurrent = 1
        ))
        assertThat(node).isNotNull
        val nodes = genesis.api.getNodes()
        val newNode = nodes.first { it.node.id == "nodeToRemove" }
        assertThat(nodes).anyMatch { it.node.id == "nodeToRemove" }
        genesis.api.removeNode(newNode.node.id)
        val updatedNodes = genesis.api.getNodes()
        assertThat(updatedNodes).noneMatch { it.node.id == "nodeToRemove" }
    }
    
    @Test
    fun `add multiple nodes`() {
        val (_, genesis) = initCluster()
        val node1Name = "multinode1"
        val node2Name = "multinode2"
        val (node1, node2) = genesis.api.addNodes(listOf(InferenceNode(
            host = "http://localhost:8080",
            pocPort = 100,
            inferencePort = 200,
            models = listOf("model1"),
            id = node1Name,
            maxConcurrent = 1
        ), InferenceNode(
            host = "http://localhost:8080",
            pocPort = 100,
            inferencePort = 200,
            models = listOf("model1"),
            id = node2Name,
            maxConcurrent = 1
        )))
        assertThat(node1).isNotNull
        assertThat(node2).isNotNull
        val nodes = genesis.api.getNodes()
        assertThat(nodes).anyMatch { it.node.id == node1Name }
        assertThat(nodes).anyMatch { it.node.id == node2Name }
    }
}
