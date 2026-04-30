import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.EpochStage
import com.productscience.LocalInferencePair
import com.productscience.data.EpochBLSData
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class BLSNetworkRecoveryTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    fun `network error recovery for tx retry and epoch query`() {
        logSection("Testing transient API outage recovery during VERIFYING")

        val (cluster, genesis) = initCluster(joinCount = 2, reboot = true)
        val allPairs = listOf(genesis) + cluster.joinPairs
        allPairs.forEach { it.waitForFirstBlock() }
        val pairsByAddress = allPairs.associateBy { it.node.getColdAddress() }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val scenario = waitForVerifyingEpochWithPendingParticipant(
            genesis = genesis,
            pairsByAddress = pairsByAddress,
            expectedParticipants = allPairs.size
        )

        stopApiContainer(scenario.participant)
        genesis.node.waitForNextBlock(1)
        startApiContainer(scenario.participant)

        waitForEpochFinalized(genesis, scenario.epochId)

        val finalEpoch = genesis.node.queryBLSEpochData(scenario.epochId).epochData
        assertThat(finalEpoch.dkgPhase.contains("COMPLETED") || finalEpoch.dkgPhase.contains("SIGNED")).isTrue()
        assertThat(finalEpoch.groupPublicKey).isNotBlank()
        assertThat(hasSubmittedVerification(finalEpoch, scenario.participantIndex)).isTrue()
    }

    private data class RecoveryScenario(
        val epochId: Long,
        val participantIndex: Int,
        val participant: LocalInferencePair
    )

    private fun waitForVerifyingEpochWithPendingParticipant(
        genesis: LocalInferencePair,
        pairsByAddress: Map<String, LocalInferencePair>,
        expectedParticipants: Int,
        maxAttempts: Int = 100
    ): RecoveryScenario {
        repeat(maxAttempts) {
            val base = genesis.getCurrentBlockHeight() / genesis.getEpochLength()
            val candidates = (base - 3..base + 4).filter { it >= 1 }
            candidates.forEach { epochId ->
                val epochData = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.getOrNull()
                    ?: return@forEach
                if (!epochData.dkgPhase.contains("VERIFYING")) return@forEach
                if (epochData.participants.size < expectedParticipants) return@forEach

                for (participantIndex in epochData.participants.indices) {
                    if (hasSubmittedVerification(epochData, participantIndex)) continue
                    val participantAddress = epochData.participants[participantIndex].address
                    val participant = pairsByAddress[participantAddress] ?: continue
                    return RecoveryScenario(
                        epochId = epochId,
                        participantIndex = participantIndex,
                        participant = participant
                    )
                }
            }
            genesis.node.waitForNextBlock(1)
        }
        error("Timeout waiting for VERIFYING epoch with pending participant")
    }

    private fun hasSubmittedVerification(epochData: EpochBLSData, participantIndex: Int): Boolean {
        val submission = epochData.verificationSubmissions?.getOrNull(participantIndex)
        return !submission?.dealerValidity.isNullOrEmpty()
    }

    private fun waitForEpochFinalized(
        pair: LocalInferencePair,
        epochId: Long,
        maxAttempts: Int = 120
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
        error("Timeout waiting for finalization in epoch $epochId")
    }

    private fun stopApiContainer(pair: LocalInferencePair) {
        setApiContainerRunning(pair, shouldRun = false)
    }

    private fun startApiContainer(pair: LocalInferencePair) {
        setApiContainerRunning(pair, shouldRun = true)
    }

    private fun setApiContainerRunning(pair: LocalInferencePair, shouldRun: Boolean) {
        val cleanName = pair.name.trimStart('/')
        val targetContainerName = "/${cleanName}-api"
        val dockerClient = DockerClientBuilder.getInstance().build()
        val container = dockerClient.listContainersCmd().withShowAll(true).exec().firstOrNull { c ->
            c.names.any { it == targetContainerName }
        } ?: error("API container not found for $cleanName")

        if (shouldRun) {
            if (container.state != "running") {
                dockerClient.startContainerCmd(container.id).exec()
            }
        } else if (container.state == "running") {
            dockerClient.stopContainerCmd(container.id).exec()
        }
    }
}
