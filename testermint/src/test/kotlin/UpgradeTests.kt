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
        val checksum = "32620280f4b6abe013e97a521ae48f1c6915c78a51cc6661c51c429951fe6032"
        val path = getBinaryPath("v2/inferenced/inferenced.zip", checksum)
        val apiCheckshum = "06ba4bb537ce5e139edbd4ffdac5d68acc5e5bc1da89b4989f12c5fe1919118b"
        val apiPath = getBinaryPath("v2/dapi/decentralized-api.zip", apiCheckshum)
        val response = highestFunded.node.submitUpgradeProposal(
            title = "v0.0.2test",
            description = "For testing",
            binaryPath = path,
            apiBinaryPath = apiPath,
            height = height + 15
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

    fun getBinaryPath(path: String, sha: String): String {
        return "http://binary-server/$path?checksum=sha256:$sha"
    }
}
