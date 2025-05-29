import com.productscience.getK8sInferencePairs
import com.productscience.inferenceConfig
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.Logger
import java.io.File
import java.net.URL
import java.time.Duration

@Tag("unstable")
class KubernetesTests : TestermintTest() {
    @Test
    fun initKubernetes() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val govParams = genesis.node.getGovParams()
            Logger.info("Gov Params: $govParams", "")
            println(govParams)
            val nodes = genesis.api.getNodes()
            println(nodes)
        }
    }

    @Test
    fun k8sGetVotes() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val proposals = genesis.node.getGovernanceProposals()
            val votes = genesis.node.getGovVotes(proposals.proposals.last().id)
            println("VOTES!" + votes)
        }
    }

    @Test
    fun k8sGetUpgrades() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val proposals = genesis.node.getGovernanceProposals()
            Logger.info("Proposals: $proposals", "")
//            k8sPairs.forEach {
//                logSection("Voting yes:" + it.name)
//                it.voteOnProposal(proposals.proposals.last().id, "yes")
//            }
        }
    }

    @Test
    fun k8sUpgrade() {
        val releaseTag = System.getenv("RELEASE_TAG") ?: "v0.1.5"

        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val govParams = genesis.node.getGovParams()
            val height = genesis.getCurrentBlockHeight()
            val upgradeBlock =
                height + govParams.params.votingPeriod.toSeconds() / 5 + 150 // the 50 ensures we aren't on an Epoch boundary
            val amdApiPath = getGithubPath(releaseTag, "decentralized-api-amd64.zip")
            val armApiPath = getGithubPath(releaseTag, "decentralized-api-arm64.zip")
            val amdBinaryPath = getGithubPath(releaseTag, "inferenced-amd64.zip")
            val armBinaryPath = getGithubPath(releaseTag, "inferenced-arm64.zip")
            Logger.info("Upgrading to $releaseTag at block $upgradeBlock", "")
            val deposit = govParams.params.minDeposit.first().amount
            logSection("Submitting upgrade proposal")
            val response = genesis.submitUpgradeProposal(
                title = releaseTag,
                description = "Automated upgrade to latest release",
                binaries = mapOf(
                    "linux/amd64" to amdBinaryPath,
                    "linux/arm64" to armBinaryPath
                ),
                apiBinaries = mapOf(
                    "linux/amd64" to amdApiPath,
                    "linux/arm64" to armApiPath
                ),
                height = upgradeBlock,
                nodeVersion = "",
                deposit = deposit.toInt()
            )
            val proposalId = response.getProposalId()
            if (proposalId == null) {
                assert(false)
                return
            }
            logSection("Making deposit")
            val depositResponse = genesis.makeGovernanceDeposit(proposalId, deposit)
            logSection("Voting on proposal")
            k8sPairs.forEach {
                val response2 = it.voteOnProposal(proposalId, "yes")
                assertThat(response2).isNotNull()
                println("VOTE:\n" + response2)
            }
            logSection("Waiting for upgrade to be effective at block $upgradeBlock")
            genesis.node.waitForMinimumBlock(upgradeBlock - 2, "upgradeBlock")
            logSection("Waiting for upgrade to finish")
            Thread.sleep(Duration.ofMinutes(5))
            logSection("Verifying upgrade")
            genesis.node.waitForNextBlock(1)
        }
    }

    private fun downloadFile(url: String, fileName: String) {
        val tempDir = File("downloads").apply { mkdirs() }
        val outputFile = File(tempDir, fileName)
        URL(url).openStream().use { input ->
            outputFile.outputStream().use { output ->
                input.copyTo(output)
            }
        }
    }

    private fun getGithubPath(releaseTag: String, fileName: String): String {
        val path = "https://github.com/product-science/race-releases/releases/download/release%2F$releaseTag/$fileName"
        val tempDir = File("downloads")
        downloadFile(path, fileName)
        val sha = getSha256Checksum(File(tempDir, fileName).absolutePath)
        return "$path?checksum=sha256:$sha"
    }
}
