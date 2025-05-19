
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.initialize
import com.productscience.initializeCluster
import com.productscience.setupLocalCluster
import org.junit.jupiter.api.Test
import kotlin.test.assertNotNull
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag

// These are for testing the infrastructure, not features
@Tag("exclude")
class InitializationTests : TestermintTest() {

    @Test
    fun initOrReuse() {
        val localCluster = setupLocalCluster(2, inferenceConfig)
        assertNotNull(localCluster)
    }

    @Test
    fun addNewJoin() {
        val localCluster = setupLocalCluster(2, inferenceConfig)
        val newCluster = localCluster.withAdditionalJoin()
        assertThat(localCluster.allPairs.size + 1).isEqualTo(newCluster.allPairs.size)
    }

    @Test
    fun reboot() {
        initCluster(reboot = true)
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
