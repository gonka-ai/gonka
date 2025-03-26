import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.initialize
import com.productscience.validNode
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.Logger

val minDeposit = 10000000L

class UpgradeTests : TestermintTest() {
    @Test
    @Tag("unstable")
    fun `submit upgrade`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val genesis = initialize(pairs)
        val height = genesis.getCurrentBlockHeight()
        val checksum = "29c7cc8e000413de302c828cc798405fa690bdaa48a2266f3d8b50a58fe62554"
        val path = getBinaryPath("v2/inferenced/inferenced.zip", checksum)
        val apiCheckshum = "18df80363d3959000e5268e56b995d5e167d2bcb4a828f0c7fb54f2a0d546e24"
        val apiPath = getBinaryPath("v2/dapi/decentralized-api.zip", apiCheckshum)
        val upgradeBlock = height + 15
        Logger.info("Upgrade block: $upgradeBlock", "")
        val response = genesis.node.submitUpgradeProposal(
            title = "v0.0.2",
            description = "For testing",
            binaryPath = path,
            apiBinaryPath = apiPath,
            height = upgradeBlock
        )
        val proposalId = response.getProposalId()
        if (proposalId == null) {
            assert(false)
            return
        }
        val depositResponse = genesis.node.makeGovernanceDeposit(proposalId, minDeposit)
        println("DEPOSIT:\n" + depositResponse)
        pairs.forEach {
            val response2 = it.node.voteOnProposal(proposalId, "yes")
            assertThat(response2).isNotNull()
            println("VOTE:\n" + response2)
        }
        genesis.node.waitForMinimumBlock(upgradeBlock)
    }

    @Test
    fun `test add node`() {
        val (pairs, genesis) = initCluster()

        pairs.allPairs.forEach {
            it.api.addNode(validNode.copy(host = "${it.name.trim('/')}-wiremock", pocPort = 8080, inferencePort = 8080,
                id = "v1Node"
            ))
        }

        genesis.node.waitForNextBlock(50)
    }

    fun getBinaryPath(path: String, sha: String): String {
        return "http://genesis-wiremock:8080/$path?checksum=sha256:$sha"
    }
}
