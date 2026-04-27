import com.productscience.EpochStage
import com.productscience.inferenceConfig
import com.productscience.logSection
import com.productscience.setupLocalCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class BLSDKGSuccessTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    @Timeout(value = 6, unit = TimeUnit.MINUTES)
    fun `BLS happy path smoke with 3 participants`() {
        logSection("Starting BLS happy path smoke test")

        // 2 participants is not enough with >50% quorum and self-exclusion for dealer approval.
        val cluster = setupLocalCluster(2, inferenceConfig, reboot = false)
        cluster.allPairs.forEach { it.waitForFirstBlock() }

        val genesis = cluster.genesis
        val allPairs = listOf(genesis) + cluster.joinPairs
        assertThat(allPairs).hasSize(3)

        logSection("Triggering DKG initiation")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val epochId = waitForSuccessfulDkgEpoch(genesis, expectedParticipants = allPairs.size)

        val blsData = genesis.node.queryBLSEpochData(epochId).epochData
        assertThat(blsData.dkgPhase.contains("COMPLETED") || blsData.dkgPhase.contains("SIGNED")).isTrue()
        assertThat(blsData.groupPublicKey).isNotBlank()
        assertThat(blsData.validDealers).isNotNull()
        assertThat(blsData.validDealers!!.any { it }).isTrue()
    }

    private fun waitForSuccessfulDkgEpoch(
        genesis: com.productscience.LocalInferencePair,
        expectedParticipants: Int,
        maxAttempts: Int = 80
    ): Long {
        repeat(maxAttempts) {
            val base = genesis.getCurrentBlockHeight() / genesis.getEpochLength()
            val candidates = (base - 3..base + 4).filter { it >= 1 }
            candidates.forEach { epochId ->
                val epochData = runCatching { genesis.node.queryBLSEpochData(epochId).epochData }.getOrNull()
                    ?: return@forEach
                if (epochData.participants.size < expectedParticipants) return@forEach
                val phase = epochData.dkgPhase
                if ((phase.contains("COMPLETED") || phase.contains("SIGNED")) && !epochData.groupPublicKey.isNullOrBlank()) {
                    return epochId
                }
            }
            genesis.node.waitForNextBlock(1)
        }
        error("Timeout waiting for successful DKG epoch with $expectedParticipants participants")
    }
}
