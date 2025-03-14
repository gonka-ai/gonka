import com.productscience.initCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ParticipantTests : TestermintTest() {
    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        val startStats = genesis.node.getParticipantCurrentStats()
        runParallelInferences(genesis, 10)
        val endStats = genesis.node.getParticipantCurrentStats()

        val statsPairs = startStats.participantCurrentStats.zip(endStats.participantCurrentStats)
        statsPairs.forEach { (start, end) ->
            assertThat(end.reputation).isGreaterThan(start.reputation)
            assertThat(end.participantId).isEqualTo(start.participantId)
        }
    }

    @Test
    fun `traffic basis decreases minimum average validation`() {
        val (_, genesis) = initCluster()
        var startMin = genesis.node.getMinimumValidationAverage()
        if (startMin.trafficBasis >= 10) {
            // Wait for current and previous values to no longer apply
            genesis.node.waitForMinimumBlock(startMin.blockHeight + genesis.getEpochLength() * 2)
            startMin = genesis.node.getMinimumValidationAverage()
        }
        genesis.waitForNextSettle()
        runParallelInferences(genesis, 50, waitForBlocks = 1)
        genesis.waitForBlock(2) {
            it.node.getMinimumValidationAverage().minimumValidationAverage < startMin.minimumValidationAverage
        }
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }
}
