import com.productscience.*
import com.productscience.data.CreatePartialUpgrade
import com.productscience.data.GovernanceProposal
import com.productscience.data.InferenceNode
import com.productscience.data.TxResponse
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.Logger
import java.util.concurrent.TimeUnit

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
        val response = genesis.submitUpgradeProposal(
            title = "v0.0.2",
            description = "For testing",
            binaryPath = path,
            apiBinaryPath = apiPath,
            height = upgradeBlock,
            nodeVersion = "",
        )
        val proposalId = response.getProposalId()
        if (proposalId == null) {
            assert(false)
            return
        }
        val depositResponse = genesis.makeGovernanceDeposit(proposalId, minDeposit)
        println("DEPOSIT:\n" + depositResponse)
        pairs.forEach {
            val response2 = it.voteOnProposal(proposalId, "yes")
            assertThat(response2).isNotNull()
            println("VOTE:\n" + response2)
        }
        genesis.node.waitForMinimumBlock(upgradeBlock)
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.MINUTES)
    fun partialUpgrade() {
        val (cluster, genesis) = initCluster(reboot = true)
        val effectiveHeight = genesis.getCurrentBlockHeight() + 40
        val newResponse = "Only a short response"
        val newSegment = "/newVersion"
        val newVersion = "v1"
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(newResponse),
                segment = newSegment
            )
            it.api.addNode(validNode.copy(host = "${it.name.trim('/')}-wiremock", pocPort = 8080, inferencePort = 8080,
                 inferenceSegment = newSegment, version = newVersion, id = "v1Node"
            ))
        }
        val inferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(inferenceResponse.choices.first().message.content).isNotEqualTo(newResponse)
        val result: TxResponse = genesis.submitGovernanceProposal(
            GovernanceProposal(
                metadata = "https://www.yahoo.com",
                deposit = "${minDeposit}${inferenceConfig.denom}",
                title = "Test Proposal",
                summary = "Test Proposal Summary",
                expedited = false,
                listOf(
                    CreatePartialUpgrade(
                        height = effectiveHeight.toString(),
                        nodeVersion = newVersion,
                        apiBinariesJson = ""
                    )
                )
            )
        )
        val proposalId = result.getProposalId()!!
        val depositResponse = genesis.makeGovernanceDeposit(proposalId, minDeposit)
        Logger.info("DEPOSIT:\n{}", depositResponse)
        cluster.joinPairs.forEach {
            val response2 = it.voteOnProposal(proposalId, "yes")
            assertThat(response2).isNotNull()
            println("VOTE:\n" + response2)
        }
        genesis.node.waitForMinimumBlock(effectiveHeight + 10)
        val newResult = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(newResult.choices.first().message.content).isEqualTo(newResponse)
    }

    fun getBinaryPath(path: String, sha: String): String {
        return "http://genesis-wiremock:8080/$path?checksum=sha256:$sha"
    }
}
