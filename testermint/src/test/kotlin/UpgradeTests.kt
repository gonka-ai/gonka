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
        val checksum = "2067b6d330ef1d1d0037a769ebec146788a2e006c4c88d709ff0fbec6f13daef"
        val path = getBinaryPath("v2/inferenced.zip", checksum)
        val apiCheckshum = "76c0767c1e3b1635e7860b58d594e82daccf0a0786a1b99782e4ca7f4ae3a3e8"
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
