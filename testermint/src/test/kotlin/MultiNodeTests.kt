import com.productscience.LocalInferencePair
import com.productscience.data.*
import com.productscience.initCluster
import com.productscience.validNode
import org.assertj.core.api.Assertions.assertThat
import org.bouncycastle.asn1.cmp.Challenge.Rand
import org.junit.jupiter.api.Test
import kotlin.random.Random

class MultiNodeTests : TestermintTest() {
    val noCappingSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0) // Disable power capping
            }
        }
    }

    @Test
    fun `more nodes, more power`() {
        val (cluster, genesis) = initCluster(mergeSpec = noCappingSpec, resetMlNodes = false, reboot = true)
        genesis.waitForNextEpoch()
        val beforeStats = genesis.node.getParticipantCurrentStats()
        println(beforeStats.participantCurrentStats?.first()?.weight)
        val node1Name = "multinode1"
        val node2Name = "multinode2"
        val nodesToAdd = 100
        val (node1, node2, node3) = genesis.addNodes(nodesToAdd)
        assertThat(node1).isNotNull
        assertThat(node2).isNotNull
        assertThat(node3).isNotNull
        val nodes = genesis.api.getNodes()

        assertThat(nodes).anyMatch { it.node.id == node1Name }
        assertThat(nodes).anyMatch { it.node.id == node2Name }
        assertThat(nodes).hasSize(nodesToAdd+1)
        val randomWeights = nodes.map {
            val weight = Random.nextInt(1, 20)
            genesis.setPocWeight(weight.toLong(), it.node)
            weight
        }
        cluster.joinPairs.forEach { pair ->
            pair.addNodes(Random.nextInt(50, 100))
            val joinNodes = pair.api.getNodes()
            joinNodes.map {
                pair.setPocWeight(Random.nextInt(1, 20).toLong(), it.node)
            }
        }
        genesis.waitForNextEpoch()
        val stats = genesis.node.getParticipantCurrentStats()
        val genesisStatus = stats.getParticipant(genesis)
        val nodes2 = genesis.api.getNodes()
        assertThat(nodes2).anyMatch { it.node.id == node1Name }
        assertThat(nodes2).anyMatch { it.node.id == node2Name }
        assertThat(genesisStatus?.weight).isEqualTo(randomWeights.sum().toLong())
    }
}
