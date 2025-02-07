import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.DockerGroup
import org.junit.jupiter.api.Test
import java.nio.file.Path

class InitializationTests {
    @Test
    fun initializeDockerOnly() {

        val genesisGroup = DockerGroup(
            dockerClient = DockerClientBuilder.getInstance().build(),
            keyName = "genesis",
            port = 8080,
            nodeConfigFile = "node_payload_wiremock_genesis.json",
            isGenesis = true,
            wiremockExternalPort = 8090,
            workingDirectory = getRepoRoot(),
            genesisOverridesFile = "inference-chain/test_genesis_overrides.json"
        )

        genesisGroup.init()
    }

    @Test
    fun init3() {
        val genesisGroup = DockerGroup(
            dockerClient = DockerClientBuilder.getInstance().build(),
            keyName = "genesis",
            port = 8080,
            nodeConfigFile = "node_payload_wiremock_genesis.json",
            isGenesis = true,
            wiremockExternalPort = 8090,
            workingDirectory = getRepoRoot(),
            genesisOverridesFile = "inference-chain/test_genesis_overrides.json"
        )

        val joinGroup1 = createJoinGroup(1, genesisGroup)
        val joinGroup2 = createJoinGroup(2, genesisGroup)
        genesisGroup.init()
        Thread.sleep(40000)
        joinGroup1.init()
        joinGroup2.init()
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

    private fun createJoinGroup(iteration: Int, genesisGroup: DockerGroup): DockerGroup {
        val keyName = "join$iteration"
        return DockerGroup(
            dockerClient = DockerClientBuilder.getInstance().build(),
            keyName = keyName,
            port = 8080 + iteration,
            nodeConfigFile = "node_payload_wiremock_$keyName.json",
            isGenesis = false,
            wiremockExternalPort = 8090 + iteration,
            workingDirectory = getRepoRoot(),
            genesisOverridesFile = "inference-chain/test_genesis_overrides.json",
            genesisGroup = genesisGroup
        )
    }

    private fun getRepoRoot(): String {
        val currentDir = Path.of("").toAbsolutePath()
        return generateSequence(currentDir) { it.parent }
            .firstOrNull { it.fileName.toString() == "inference-ignite" }
            ?.toString()
            ?: throw IllegalStateException("Repository root 'inference-ignite' not found")
    }
}
