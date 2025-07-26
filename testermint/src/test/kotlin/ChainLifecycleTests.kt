import com.productscience.getRepoRoot
import com.productscience.initCluster
import com.productscience.logSection
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertTimeoutPreemptively
import org.tinylog.kotlin.Logger
import java.io.File
import java.time.Duration

class ChainLifecycleTests : TestermintTest() {

    @Test
    fun `reproduce consensus failure on node reconnect`() {
        val (cluster, genesis) = initCluster(joinCount = 2)

        logSection("Waiting for first PoC to be over")
        genesis.waitForNextInferenceWindow()

        val nodeToDisconnect = cluster.joinPairs.last()
        logSection("Disconnecting node ${nodeToDisconnect.name}")
        killNodeContainers(nodeToDisconnect.name.trim('/'))

        logSection("Adding a new node")
        val newCluster = cluster.withAdditionalJoin(1)

        logSection("Waiting for new blocks to see if chain is alive")
        assertTimeoutPreemptively(Duration.ofSeconds(60), "Chain did not produce blocks, consensus failure likely.") {
            newCluster.genesis.node.waitForNextBlock(5)
        }
    }

    private fun killNodeContainers(keyName: String) {
        val repoRoot = getRepoRoot()

        val psProcess = ProcessBuilder("docker", "ps", "-a", "-q", "--filter", "name=$keyName")
            .directory(File(repoRoot))
            .start()
        val containerIds = psProcess.inputStream.bufferedReader().readLines()
        psProcess.waitFor()

        if (containerIds.isNotEmpty()) {
            val killArgs = mutableListOf("docker", "kill")
            killArgs.addAll(containerIds)
            val killProcess = ProcessBuilder(*killArgs.toTypedArray())
                .directory(File(repoRoot))
                .start()
            val output = killProcess.inputStream.bufferedReader().readText()
            killProcess.waitFor()
            Logger.info("Killed containers for $keyName: $output")
        } else {
            Logger.warn("No containers found to kill for $keyName")
        }
    }
}
