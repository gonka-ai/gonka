import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initialize
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

val minDeposit = 10000000L

class UpgradeTests : TestermintTest() {
    @Test
    fun `submit upgrade`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val height = highestFunded.getCurrentBlockHeight()
        val path = "http://binary-server/v2/inferenced.zip?checksum=sha256:a39a573678a2e29227c3aa1ce3f8fb2e913b43679680366a427a91cbd5d3f8d3"
        val response = highestFunded.node.submitUpgradeProposal(
            title = "v0.0.2test",
            description = "For testing",
            binaryPath = path,
            height = height + 20
        )
        val proposalId = response.getProposalId()
        if (proposalId == null) {
            assert(false)
            return
        }
        val depositResponse = highestFunded.node.makeGovernanceDeposit(proposalId, minDeposit)
        println("DEPOSIT:\n" + depositResponse)
        pairs.forEach {
            val response2 = it.node.voteOnProposal(proposalId, "yes")
            assertThat(response2).isNotNull()
            println("VOTE:\n" + response2)
        }
    }
}
