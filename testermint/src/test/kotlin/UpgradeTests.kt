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
        genesis.waitForNextInferenceWindow()
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
        genesis.waitForNextInferenceWindow()
        val proposals = genesis.node.getGovernanceProposals()
        Logger.info("Proposals: $proposals", "")
        val newResult = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(newResult.choices.first().message.content).isEqualTo(newResponse)
    }


    @Test
    @Timeout(value = 15, unit = TimeUnit.MINUTES)
    fun testVersionedEndpointSwitching() {
        val (cluster, genesis) = initCluster(reboot = true)

        logSection("Waiting for initial system to be ready")
        var currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })
        
        // Test that the system works initially before we modify it
        logSection("Verifying system is working before version changes")
        val systemCheckResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(systemCheckResponse.choices.first().message.content).isNotEmpty()
        
        logSection("Setting up versioned mock responses")
        
        // Define unique responses for each version to clearly distinguish them
        val v038Response = "Response from version v3.0.8"
        val v039Response = "Response from version v3.0.9"
        val v0310Response = "Response from version v3.0.10"
        // Remove version prefixes from segments - they will be added by InferenceUrlWithVersion

        val chatCompletionStr = "/v1/chat/completions"
        val initialVersion = "v3.0.8"
        val firstUpgradeVersion = "v3.0.9"
        val secondUpgradeVersion = "v3.0.10"
        
        // Configure mock servers with version-specific responses for all segments
        cluster.allPairs.forEach { pair ->
            // Set up default non-versioned endpoint (current behavior)
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse("Default response")
            )
            // Set up v3.0.8 versioned endpoints 
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v038Response),
                segment = "v3.0.8"
            )
            // Set up v3.0.9 versioned endpoints
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v039Response),
                segment = "v3.0.9"
            )
            // Set up v3.0.10 versioned endpoints
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v0310Response),
                segment = "v3.0.10"
            )
        }
        
        logSection("Testing initial version v3.0.8 - should use default endpoints")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })
        val initialInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        // Initially should use non-versioned endpoints, so default response
        assertThat(initialInferenceResponse.choices.first().message.content).isNotEmpty()
        
        logSection("Initiating first upgrade: v3.0.8 → v3.0.9")
        val firstUpgradeHeight = genesis.getCurrentBlockHeight() + 10
        
        val firstProposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = firstUpgradeHeight.toString(),
                nodeVersion = firstUpgradeVersion,
                apiBinariesJson = ""
            )
        )
        
        logSection("Waiting for first upgrade to take effect at height $firstUpgradeHeight")
        genesis.node.waitForMinimumBlock(firstUpgradeHeight + 1, "firstUpgradeHeight+10")
        
        logSection("Testing post-upgrade requests should hit v3.0.9 endpoints")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })
        val upgradedInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(upgradedInferenceResponse.choices.first().message.content)
            .withFailMessage("After first upgrade, inference should use v3.0.9 endpoint")
            .isEqualTo(v039Response)
        
        // Verify that the correct versioned URLs are being called
        logSection("Verifying v3.0.9 URLs are being used")
        cluster.allPairs.forEach { pair ->
            val hasV039Requests = pair.mock?.hasRequestsToVersionedEndpoint("") ?: false
            Logger.info("Node ${pair.name} received requests to v3.0.9 inference endpoints: $hasV039Requests", "")
            assertThat(hasV039Requests)
                .withFailMessage("Expected node ${pair.name} to receive requests on v3.0.9 inference endpoints")
                .isTrue()
        }
        
        logSection("Initiating second upgrade: v3.0.9 → v3.0.10")
        val secondUpgradeHeight = genesis.getCurrentBlockHeight() + 10
        
        val secondProposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = secondUpgradeHeight.toString(),
                nodeVersion = secondUpgradeVersion,
                apiBinariesJson = ""
            )
        )
        
        logSection("Waiting for second upgrade to take effect at height $secondUpgradeHeight")
        genesis.node.waitForMinimumBlock(secondUpgradeHeight + 10, "secondUpgradeHeight+10")
        
        logSection("Testing post-second-upgrade requests should hit v3.0.10 endpoints")
        genesis.waitForNextInferenceWindow()
        val finalInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(finalInferenceResponse.choices.first().message.content)
            .withFailMessage("After second upgrade, inference should use v3.0.10 endpoint")
            .isEqualTo(v0310Response)
        
        // Verify that the correct versioned URLs are being called
        logSection("Verifying v3.0.10 URLs are being used")
        cluster.allPairs.forEach { pair ->
            val hasV0310Requests = pair.mock?.hasRequestsToVersionedEndpoint("/") ?: false
            Logger.info("Node ${pair.name} received requests to v3.0.10 inference endpoints: $hasV0310Requests", "")
            assertThat(hasV0310Requests)
                .withFailMessage("Expected node ${pair.name} to receive requests on v3.0.10 inference endpoints")
                .isTrue()
        }
        
        logSection("Verifying API endpoints are also routing correctly")
        // Test that API calls (like getting nodes) also work correctly after version switching
        cluster.allPairs.forEach { pair ->
            val nodesList = pair.api.getNodes()
            assertThat(nodesList).isNotEmpty()
            Logger.info("Node ${pair.name} successfully retrieved nodes list with ${nodesList.size} nodes", "")
        }
        
        logSection("All version switching tests completed successfully: v3.0.8 → v3.0.9 → v3.0.10")
    }

    fun getBinaryPath(path: String): String {
        val localPath = "../public-html/$path"
        val sha = getSha256Checksum(localPath)
        return "http://genesis-mock-server:8080/files/$path?checksum=sha256:$sha"
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
