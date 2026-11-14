import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.cosmosJson
import com.productscience.data.EpochBLSDataWrapper
import com.productscience.inferenceConfig
import com.productscience.logSection
import com.productscience.setupLocalCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class BLSDKGSuccessTest : TestermintTest() {

    private lateinit var cluster: LocalCluster

    @BeforeEach
    @Timeout(value = 6, unit = TimeUnit.MINUTES)
    fun prepareCluster() {
        // Config discovery may rebuild the cluster even when reboot is false.
        // Bound preparation separately so it cannot consume the DKG assertion budget.
        logSection("Preparing BLS smoke cluster")
        // 2 participants is not enough with >50% quorum and self-exclusion for dealer approval.
        cluster = setupLocalCluster(2, inferenceConfig, reboot = false)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        logSection("BLS smoke cluster ready")
    }

    @Test
    @Tag("bls-integration")
    @Timeout(value = 6, unit = TimeUnit.MINUTES)
    fun `BLS happy path smoke with 3 participants`() {
        logSection("Starting BLS happy path smoke test")

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
        var selectedEpoch: Long? = null
        repeat(maxAttempts) {
            val base = genesis.getCurrentBlockHeight() / genesis.getEpochLength()
            val candidates = selectedEpoch?.let { listOf(it) }
                ?: (base - 3..base + 4).filter { it >= 1 }
            for (epochId in candidates) {
                val epochData = if (selectedEpoch != null) {
                    // Once selected, a query failure must not be mistaken for an absent epoch.
                    genesis.node.queryBLSEpochData(epochId).epochData
                } else {
                    val output = genesis.node.execCli(listOf("query", "bls", "epoch-data", epochId.toString())).trim()
                    // Future epochs legitimately have no DKG data. Transport errors,
                    // malformed JSON and interruptions must not skip an eligible epoch.
                    if (output.startsWith("rpc error: code = NotFound desc = ") &&
                        output.endsWith("no DKG data found for epoch $epochId: key not found")) continue
                    cosmosJson.fromJson(output, EpochBLSDataWrapper::class.java).epochData
                }
                if (epochData.participants.size < expectedParticipants) continue
                if (selectedEpoch == null) {
                    selectedEpoch = epochId
                    logSection("Waiting for DKG completion of epoch $epochId with $expectedParticipants participants")
                }
                val phase = epochData.dkgPhase
                check(!phase.contains("FAILED")) { "DKG failed for selected epoch $epochId: $phase" }
                if ((phase.contains("COMPLETED") || phase.contains("SIGNED")) && !epochData.groupPublicKey.isNullOrBlank()) {
                    return epochId
                }
                // Do not accept a later epoch if the first eligible one cannot complete.
                break
            }
            genesis.node.waitForNextBlock(1)
        }
        error("Timeout waiting for successful DKG epoch $selectedEpoch with $expectedParticipants participants")
    }
}
