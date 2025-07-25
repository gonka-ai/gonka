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
        genesis.markNeedsReboot()
        
        logSection("Waiting for initial system to be ready")
        genesis.waitForNextInferenceWindow()
        
        // Test that the system works initially before we modify it
        logSection("Verifying system is working before version changes")
        val systemCheckResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(systemCheckResponse.choices.first().message.content).isNotEmpty()
        
        logSection("Setting up versioned endpoints with unique responses")
        
        // Define unique responses for each version to clearly distinguish them
        val v038Response = "Response from version 0.3.8"
        val v039Response = "Response from version 0.3.9"  
        val v0310Response = "Response from version 0.3.10"
        val v038Segment = "/v0.3.8"
        val v039Segment = "/v0.3.9"
        val v0310Segment = "/v0.3.10"
        val initialVersion = "v0.3.8"
        val firstUpgradeVersion = "v0.3.9"
        val secondUpgradeVersion = "v0.3.10"
        
        // Configure mock servers with version-specific responses
        cluster.allPairs.forEach { pair ->
            // Set up v0.3.8 endpoints
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v038Response),
                segment = v038Segment
            )
            // Set up v0.3.9 endpoints  
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v039Response),
                segment = v039Segment
            )
            // Set up v0.3.10 endpoints
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v0310Response),
                segment = v0310Segment
            )
        }
        
        // Add versioned nodes alongside existing ones (following partialUpgrade pattern)
        logSection("Adding v0.3.8 versioned nodes")
        cluster.allPairs.forEach { pair ->
            // Add new node with v0.3.8 version configuration
            pair.api.addNode(
                validNode.copy(
                    host = "${pair.name.trim('/')}-mock-server",
                    pocPort = 8080,
                    inferencePort = 8080,
                    inferenceSegment = v038Segment,
                    pocSegment = "/api/v1",
                    version = initialVersion,
                    id = "${initialVersion}Node"
                )
            )
            
            // Set up default inference response for v0.3.8
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v038Response),
                streamDelay = Duration.ofMillis(200)
            )
        }
        
        logSection("Testing initial version v0.3.8 requests")
        genesis.waitForNextInferenceWindow()
        val initialInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(initialInferenceResponse.choices.first().message.content)
            .withFailMessage("Initial inference should use v0.3.8 endpoint")
            .isEqualTo(v038Response)
        
        logSection("Initiating first upgrade: v0.3.8 → v0.3.9")
        val firstUpgradeHeight = genesis.getCurrentBlockHeight() + 40
        
        // Add nodes with v0.3.9 configuration before upgrade
        cluster.allPairs.forEach { pair ->
            pair.api.addNode(
                validNode.copy(
                    host = "${pair.name.trim('/')}-mock-server",
                    pocPort = 8080,
                    inferencePort = 8080,
                    inferenceSegment = v039Segment,
                    pocSegment = "/api/v1", 
                    version = firstUpgradeVersion,
                    id = "${firstUpgradeVersion}Node"
                )
            )
            // Update default response for new version
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v039Response),
                streamDelay = Duration.ofMillis(200)
            )
        }
        
        val firstProposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = firstUpgradeHeight.toString(),
                nodeVersion = firstUpgradeVersion,
                apiBinariesJson = ""
            )
        )
        
        logSection("Waiting for first upgrade to take effect at height $firstUpgradeHeight")
        genesis.node.waitForMinimumBlock(firstUpgradeHeight + 10, "firstUpgradeHeight+10")
        
        logSection("Testing post-upgrade requests should hit v0.3.9 endpoints")
        genesis.waitForNextInferenceWindow()
        val upgradedInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(upgradedInferenceResponse.choices.first().message.content)
            .withFailMessage("After first upgrade, inference should use v0.3.9 endpoint")
            .isEqualTo(v039Response)
        
        logSection("Initiating second upgrade: v0.3.9 → v0.3.10")
        val secondUpgradeHeight = genesis.getCurrentBlockHeight() + 40
        
        // Add nodes with v0.3.10 configuration before upgrade
        cluster.allPairs.forEach { pair ->
            pair.api.addNode(
                validNode.copy(
                    host = "${pair.name.trim('/')}-mock-server",
                    pocPort = 8080,
                    inferencePort = 8080,
                    inferenceSegment = v0310Segment,
                    pocSegment = "/api/v1",
                    version = secondUpgradeVersion,
                    id = "${secondUpgradeVersion}Node"
                )
            )
            // Update default response for new version
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse(v0310Response),
                streamDelay = Duration.ofMillis(200)
            )
        }
        
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
        
        logSection("Testing post-second-upgrade requests should hit v0.3.10 endpoints")
        genesis.waitForNextInferenceWindow()
        val finalInferenceResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(finalInferenceResponse.choices.first().message.content)
            .withFailMessage("After second upgrade, inference should use v0.3.10 endpoint")
            .isEqualTo(v0310Response)
        
        logSection("Verifying API endpoints are also routing correctly")
        // Test that API calls (like getting nodes) also work correctly with versioned routing
        cluster.allPairs.forEach { pair ->
            val nodesList = pair.api.getNodes()
            assertThat(nodesList).isNotEmpty()
            Logger.info("Node ${pair.name} successfully retrieved nodes list with ${nodesList.size} nodes", "")
        }
        
        logSection("All version switching tests completed successfully: v0.3.8 → v0.3.9 → v0.3.10")
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
