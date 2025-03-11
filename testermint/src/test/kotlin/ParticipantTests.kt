import com.productscience.initCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ParticipantTests {
    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        val startStats = genesis.node.getParticipantCurrentStats()
        runParallelInferences(genesis, 100)
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
        if (startMin.trafficBasis >= 1000) {
            // Wait for current and previous values to no longer apply
            genesis.node.waitForMinimumBlock(startMin.blockHeight + genesis.getEpochLength()*2)
            startMin = genesis.node.getMinimumValidationAverage()
        }
        runParallelInferences(genesis, 100)
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }
}
