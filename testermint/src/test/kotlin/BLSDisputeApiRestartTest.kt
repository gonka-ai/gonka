import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.EpochStage
import com.productscience.LocalInferencePair
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class BLSDisputeApiRestartTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    fun `dealer complaint response survives dealer api restart`() {
        logSection("Testing dispute phase progress after dealer API restart")

        val (cluster, genesis) = initCluster(joinCount = 2, reboot = true)
        val allPairs = listOf(genesis) + cluster.joinPairs
        allPairs.forEach { it.waitForFirstBlock() }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val epochId = waitForEpochInPhase(
            genesis = genesis,
            phaseToken = "DISPUTING",
            expectedParticipants = allPairs.size
        )

        val dealerToRestart = cluster.joinPairs.first()
        restartApiContainer(dealerToRestart)

        waitForEpochSuccessful(genesis, epochId)

        val finalEpoch = genesis.node.queryBLSEpochData(epochId).epochData
        assertThat(finalEpoch.dkgPhase.contains("COMPLETED") || finalEpoch.dkgPhase.contains("SIGNED")).isTrue()
        assertThat(finalEpoch.groupPublicKey).isNotBlank()
        assertThat(finalEpoch.validDealers).isNotNull()
        assertThat(finalEpoch.validDealers!!.any { it }).isTrue()
    }

    private fun restartApiContainer(pair: LocalInferencePair) {
        val cleanName = pair.name.trimStart('/')
        val targetContainerName = "/${cleanName}-api"
        val dockerClient = DockerClientBuilder.getInstance().build()
        val container = dockerClient.listContainersCmd().withShowAll(true).exec().firstOrNull { c ->
            c.names.any { it == targetContainerName }
        } ?: error("API container not found for $cleanName")

        if (container.state == "running") {
            dockerClient.stopContainerCmd(container.id).exec()
        }
        dockerClient.startContainerCmd(container.id).exec()
    }

    private fun waitForEpochInPhase(
        genesis: LocalInferencePair,
        phaseToken: String,
        expectedParticipants: Int,
        maxAttempts: Int = 90
    ): Long {
        repeat(maxAttempts) {
            val base = genesis.getCurrentBlockHeight() / genesis.getEpochLength()
            val candidates = (base - 3..base + 4).filter { it >= 1 }
            candidates.forEach { epochId ->
                val epoch = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.getOrNull()
                    ?: return@forEach
                if (epoch.participants.size < expectedParticipants) return@forEach
                val phase = epoch.dkgPhase
                if (phase.contains(phaseToken)) {
                    return epochId
                }
            }
            genesis.node.waitForNextBlock(1)
        }
        error("Timeout waiting for $phaseToken phase")
    }

    private fun waitForEpochSuccessful(
        pair: LocalInferencePair,
        epochId: Long,
        maxAttempts: Int = 80
    ) {
        repeat(maxAttempts) {
            val phase = runCatching { pair.node.queryBLSEpochData(epochId).epochData.dkgPhase }.getOrNull() ?: ""
            if (phase.contains("FAILED")) {
                error("DKG failed for epoch $epochId")
            }
            if (phase.contains("COMPLETED") || phase.contains("SIGNED")) {
                return
            }
            pair.node.waitForNextBlock(1)
        }
        error("Timeout waiting successful completion for epoch $epochId")
    }
}
