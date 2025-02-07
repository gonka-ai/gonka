import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initialize
import com.productscience.initializeCluster
import org.junit.jupiter.api.Test

class InitializationTests {

    @Test
    fun init3() {
        initializeCluster(2, inferenceConfig)
    }

    @Test
    fun getGenesisState() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val genesis = highestFunded.node.getGenesisState()

        println(genesis.appState.inference)
    }

    @Test
    fun shutdownAll() {
        val dockerClient = DockerClientBuilder.getInstance().build()
        val containers = dockerClient.listContainersCmd().withShowAll(true).exec()
        containers.parallelStream().forEach { container ->
            if (container.state == "running") {
                dockerClient.stopContainerCmd(container.id).exec()
            }
            dockerClient.removeContainerCmd(container.id).withForce(true).withRemoveVolumes(true).exec()
        }
        val network = dockerClient.listNetworksCmd().exec().firstOrNull { it.name == "chain-public" }
        network?.let {
            dockerClient.removeNetworkCmd(it.id).exec()
        }
    }
}
