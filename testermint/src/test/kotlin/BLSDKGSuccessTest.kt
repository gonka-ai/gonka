import com.productscience.EpochStage
import com.productscience.LocalInferencePair
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class BLSDKGSuccessTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    fun `BLS happy path smoke with 3 participants`() {
        logSection("Starting BLS happy path smoke test")

        // 2 participants is not enough with >50% quorum and self-exclusion for dealer approval.
        val (cluster, genesis) = initCluster(joinCount = 2, config = inferenceConfig, reboot = false)
        val allPairs = listOf(genesis) + cluster.joinPairs
        assertThat(allPairs).hasSize(3)

        logSection("Triggering DKG initiation")
        val epochId = waitForDkgEpochWithParticipants(genesis, expectedParticipants = allPairs.size)
        waitForEpochSuccessful(genesis, epochId)

        val blsData = genesis.node.queryBLSEpochData(epochId).epochData
        assertThat(blsData.dkgPhase.contains("COMPLETED") || blsData.dkgPhase.contains("SIGNED")).isTrue()
        assertThat(blsData.groupPublicKey).isNotBlank()
        assertThat(blsData.validDealers).isNotNull()
        assertThat(blsData.validDealers!!.any { it }).isTrue()
    }

    private fun waitForDkgEpochWithParticipants(
        genesis: LocalInferencePair,
        expectedParticipants: Int,
        maxEpochs: Int = 5
    ): Long {
        repeat(maxEpochs) {
            val epochId = genesis.getEpochData().latestEpoch.index
            if (epochId >= 1) {
                val epochData = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.getOrNull()
                if (epochData != null && epochData.participants.size >= expectedParticipants) {
                    return epochId
                }
            }
            genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        }
        val finalEpochId = genesis.getEpochData().latestEpoch.index
        val finalEpochData = runCatching { genesis.node.queryBLSEpochData(finalEpochId).epochData }.getOrNull()
        if (finalEpochData != null && finalEpochData.participants.size >= expectedParticipants) {
            return finalEpochId
        }
        error("Timeout waiting for DKG epoch with $expectedParticipants participants (last epochChecked=$finalEpochId)")
    }

    private fun waitForEpochSuccessful(
        genesis: LocalInferencePair,
        epochId: Long,
        maxAttempts: Int = 80
    ) {
        repeat(maxAttempts) {
            val epochData = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.getOrNull()
            val phase = epochData?.dkgPhase ?: ""
            if (phase.contains("FAILED")) {
                error("DKG failed for epoch $epochId")
            }
            if ((phase.contains("COMPLETED") || phase.contains("SIGNED")) && !epochData?.groupPublicKey.isNullOrBlank()) {
                return
            }
            genesis.node.waitForNextBlock(1)
        }
        error("Timeout waiting for successful DKG completion in epoch $epochId")
    }
}
