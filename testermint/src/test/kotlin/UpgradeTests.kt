import com.productscience.*
import com.productscience.data.CreatePartialUpgrade
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.Logger
import java.io.File
import java.security.MessageDigest
import java.time.Duration
import java.util.concurrent.TimeUnit
import kotlin.test.assertNotNull

class UpgradeTests : TestermintTest() {
    @Test
    @Tag("unstable")
    fun `upgrade from github`() {
        val releaseTag = "v0.1.4-25"

        val (cluster, genesis) = initCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                )
            ),
            reboot = true
        )
        genesis.markNeedsReboot()
        val pairs = cluster.joinPairs
        val height = genesis.getCurrentBlockHeight()
        val amdApiPath = getGithubPath(releaseTag, "decentralized-api-amd64.zip")
        val amdBinaryPath = getGithubPath(releaseTag, "inferenced-amd64.zip")
        val upgradeBlock = height + 30
        Logger.info("Upgrade block: $upgradeBlock", "")
        logSection("Submitting upgrade proposal")
        val response = genesis.submitUpgradeProposal(
            title = releaseTag,
            description = "For testing",
            binaryPath = amdBinaryPath,
            apiBinaryPath = amdApiPath,
            height = upgradeBlock,
            nodeVersion = "",
        )
        val proposalId = response.getProposalId()
        assertNotNull(proposalId, "couldn't find proposal")
        val govParams = genesis.node.getGovParams().params
        logSection("Making deposit")
        val depositResponse = genesis.makeGovernanceDeposit(proposalId, govParams.minDeposit.first().amount)
        logSection("Voting on proposal")
        pairs.forEach {
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
        // Some other action?
        cluster.allPairs.forEach {
            it.api.getParticipants()
            it.api.getNodes()
            it.node.getAddress()
        }

    }

    @Test
    fun `submit upgrade`() {
        val (cluster, genesis) = initCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                )
            ),
            reboot = true
        )
        genesis.markNeedsReboot()
        val pairs = cluster.joinPairs
        val height = genesis.getCurrentBlockHeight()
        val path = getBinaryPath("v2/inferenced/inferenced-amd64.zip")
        val apiPath = getBinaryPath("v2/dapi/decentralized-api-amd64.zip")
        val upgradeBlock = height + 30
        Logger.info("Upgrade block: $upgradeBlock", "")
        logSection("Submitting upgrade proposal")
        val response = genesis.submitUpgradeProposal(
            title = "v0.0.1test",
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
        val govParams = genesis.node.getGovParams().params
        logSection("Making deposit")
        val depositResponse = genesis.makeGovernanceDeposit(proposalId, govParams.minDeposit.first().amount)
        logSection("Voting on proposal")
        pairs.forEach {
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
        // Some other action?
        cluster.allPairs.forEach {
            it.api.getParticipants()
            it.api.getNodes()
            it.node.getAddress()
        }

    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.MINUTES)
    fun partialUpgrade() {
        val (cluster, genesis) = initCluster(reboot = true)
        genesis.markNeedsReboot()
        logSection("Verifying current inference hits right endpoint")
        val effectiveHeight = genesis.getCurrentBlockHeight() + 40
        val newResponse = "Only a short response"
        val newSegment = "/newVersion"
        val newVersion = "v1"
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(newResponse),
                segment = newSegment
            )
            it.api.addNode(
                validNode.copy(
                    host = "${it.name.trim('/')}-mock-server", pocPort = 8080, inferencePort = 8080,
                    inferenceSegment = newSegment, version = newVersion, id = "v1Node"
                )
            )
        }
        val inferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(inferenceResponse.choices.first().message.content).isNotEqualTo(newResponse)
        val proposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = effectiveHeight.toString(),
                nodeVersion = newVersion,
                apiBinariesJson = ""
            )
        )
        logSection("Waiting for upgrade to be effective")
        genesis.node.waitForMinimumBlock(effectiveHeight + 10, "partialUpgradeTime+10")
        logSection("Verifying new inference hits right endpoint")
        val proposals = genesis.node.getGovernanceProposals()
        Logger.info("Proposals: $proposals", "")
        val newResult = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(newResult.choices.first().message.content).isEqualTo(newResponse)
    }

    fun getBinaryPath(path: String): String {
        val localPath = "../public-html/$path"
        val sha = getSha256Checksum(localPath)
        return "http://genesis-mock-server:8080/$path?checksum=sha256:$sha"
    }

    @Test
    @Tag("unstable")
    fun blindUpgrade() {
        //        val releaseTag = System.getenv("RELEASE_TAG") ?: "release/v0.1.4-25"
        val releaseTag = "v0.1.10-test"
        val releaseVersion = releaseTag.substringAfterLast("/")
        val cluster = getLocalCluster(inferenceConfig)
        val genesis = cluster!!.genesis
        val govParams = genesis.node.getGovParams()
        val height = genesis.getCurrentBlockHeight()
        val upgradeBlock =
            height + govParams.params.votingPeriod.toSeconds() / 5 + 5 // the 50 ensures we aren't on an Epoch boundary
        val amdApiPath = getGithubPath(releaseTag, "decentralized-api-amd64.zip")
//            val armApiPath = getGithubPath(releaseTag, "decentralized-api-arm64.zip")
        val amdBinaryPath = getGithubPath(releaseTag, "inferenced-amd64.zip")
//            val armBinaryPath = getGithubPath(releaseTag, "inferenced-arm64.zip")
        Logger.info("Upgrading to $releaseTag at block $upgradeBlock", "")
        val deposit = govParams.params.minDeposit.first().amount
        logSection("Submitting upgrade proposal")
        val response = genesis.submitUpgradeProposal(
            title = releaseVersion,
            description = "Automated upgrade to latest release",
            binaries = mapOf(
                "linux/amd64" to amdBinaryPath,
//                    "linux/arm64" to armBinaryPath
            ),
            apiBinaries = mapOf(
                "linux/amd64" to amdApiPath,
//                    "linux/arm64" to armApiPath
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
        cluster.allPairs.forEach {
            val response2 = it.voteOnProposal(proposalId, "yes")
            assertThat(response2).isNotNull()
            println("VOTE:\n" + response2)
        }
        logSection("Waiting for upgrade to be effective at block $upgradeBlock")
        genesis.node.waitForMinimumBlock(upgradeBlock - 2, "upgradeBlock")
        logSection("Waiting for upgrade to finish")
        Thread.sleep(Duration.ofMinutes(5))
    }
}

fun getSha256Checksum(filePath: String): String {
    val digest = MessageDigest.getInstance("SHA-256")
    val file = File(filePath)
    file.inputStream().use { fis ->
        val buffer = ByteArray(8192)
        var bytesRead = fis.read(buffer)
        while (bytesRead != -1) {
            digest.update(buffer, 0, bytesRead)
            bytesRead = fis.read(buffer)
        }
    }
    return digest.digest().joinToString("") { "%02x".format(it) }
}


