import com.productscience.*
import com.productscience.data.CreatePartialUpgrade
import com.productscience.data.OpenAIResponse
import com.github.dockerjava.api.exception.NotFoundException
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.Logger
import java.io.File
import java.net.URL
import java.net.URLEncoder
import java.net.SocketException
import java.security.MessageDigest
import java.time.Duration
import java.util.concurrent.TimeUnit
import kotlin.test.assertNotNull

class UpgradeTests : TestermintTest() {
    private fun assertLastUpgradeHeight(pair: LocalInferencePair, expectedHeight: Long, expectedFound: Boolean = true) {
        val response = pair.node.getLastUpgradeHeight()
        assertThat(response.found).isEqualTo(expectedFound)
        assertThat(response.lastUpgradeHeight).isEqualTo(expectedHeight)
    }

    private fun assertLastUpgradeHeight(cluster: LocalCluster, expectedHeight: Long, expectedFound: Boolean = true) {
        cluster.allPairs.forEach { pair ->
            assertLastUpgradeHeight(pair, expectedHeight, expectedFound)
        }
    }

    private fun assertLastUpgradeHeightUnset(pair: LocalInferencePair) {
        assertLastUpgradeHeight(pair, 0, expectedFound = false)
    }

    private fun assertLastUpgradeHeightUnset(cluster: LocalCluster) {
        assertLastUpgradeHeight(cluster, 0, expectedFound = false)
    }

    private val upgradeArchitectures = listOf("amd64", "arm64")

    private fun initUpgradeCluster(config: ApplicationConfig = inferenceConfig): Pair<LocalCluster, LocalInferencePair> {
        var lastFailure: Throwable? = null
        repeat(3) { attempt ->
            try {
                return initCluster(config = config, reboot = true)
            } catch (t: Throwable) {
                val shouldRetry =
                    t.message?.contains("Could not find node container for keyName=genesis") == true ||
                        t.message?.contains("Failed to get validator info within 90 seconds") == true ||
                        t.message?.contains("without condition passing") == true ||
                        generateSequence(t) { it.cause }.any { it is SocketException || it is NotFoundException }
                if (!shouldRetry || attempt == 2) {
                    throw t
                }
                lastFailure = t
                Logger.warn("Upgrade cluster bootstrap failed on attempt ${attempt + 1}, retrying: ${t.message}", "")
                Thread.sleep(Duration.ofSeconds(10))
            }
        }
        throw lastFailure ?: IllegalStateException("Upgrade cluster bootstrap failed")
    }

    private fun verifyPairHealthy(pair: LocalInferencePair) {
        pair.api.getParticipants()
        pair.api.getNodes()
        pair.node.getColdAddress()
    }

    private fun pairIsOperational(pair: LocalInferencePair): Boolean =
        runCatching {
            verifyPairHealthy(pair)
            pair.api.getNodes().isNotEmpty() &&
                pair.api.getNodes().all { node ->
                    node.state.currentStatus != "UNKNOWN" && node.state.intendedStatus != "UNKNOWN"
                }
        }.getOrDefault(false)

    private fun waitForClusterOperational(cluster: LocalCluster, genesis: LocalInferencePair, maxBlocks: Int = 20) {
        val startBlock = genesis.getCurrentBlockHeight()
        val targetBlock = startBlock + maxBlocks

        while (genesis.getCurrentBlockHeight() < targetBlock) {
            if (cluster.allPairs.all(::pairIsOperational)) {
                return
            }
            genesis.node.waitForNextBlock(1)
        }

        error("Cluster did not become operational by block $targetBlock")
    }

    private fun waitForInferenceReady(
        pair: LocalInferencePair,
        request: String,
        maxBlocks: Int = 10,
    ): OpenAIResponse {
        var response: OpenAIResponse? = null
        pair.waitForBlock(maxBlocks) {
            response = runCatching { it.makeInferenceRequest(request) }.getOrNull()
            response != null
        }
        return assertNotNull(response)
    }

    private fun waitForLastUpgradeHeight(cluster: LocalCluster, genesis: LocalInferencePair, expectedHeight: Long, maxBlocks: Int = 20) {
        val startBlock = genesis.getCurrentBlockHeight()
        val targetBlock = startBlock + maxBlocks

        while (genesis.getCurrentBlockHeight() < targetBlock) {
            val allUpdated = cluster.allPairs.all { pair ->
                runCatching {
                    val response = pair.node.getLastUpgradeHeight()
                    response.found && response.lastUpgradeHeight == expectedHeight
                }.getOrDefault(false)
            }
            if (allUpdated) {
                return
            }
            genesis.node.waitForNextBlock(1)
        }

        error("LastUpgradeHeight did not become $expectedHeight by block $targetBlock")
    }

    private fun isBadGatewayFailure(t: Throwable): Boolean =
        generateSequence(t) { it.cause }.any { cause ->
            cause.message?.contains("502") == true || cause.message?.contains("Bad Gateway") == true
        }

    private fun verifyUpgradeWithLocalApiRecovery(cluster: LocalCluster, genesis: LocalInferencePair) {
        fun failedPairs(): List<LocalInferencePair> =
            cluster.allPairs.filter { pair -> runCatching { verifyPairHealthy(pair) }.isFailure }

        val initialFailures = failedPairs()
        if (initialFailures.isEmpty()) {
            return
        }

        val firstFailure = runCatching { verifyPairHealthy(initialFailures.first()) }.exceptionOrNull()
        if (firstFailure == null || !isBadGatewayFailure(firstFailure)) {
            throw firstFailure ?: IllegalStateException("Upgrade verification failed for unknown reason")
        }

        Logger.warn(
            "Post-upgrade API verification failed with 502; restarting local API containers for {}",
            initialFailures.joinToString(", ") { it.name }
        )
        initialFailures.forEach { it.restartApiContainer() }

        genesis.waitForBlock(20) {
            cluster.allPairs.all { pair ->
                runCatching {
                    verifyPairHealthy(pair)
                    true
                }.getOrDefault(false)
            }
        }

        cluster.allPairs.forEach(::verifyPairHealthy)
    }

    @Test
    @Tag("unstable")
    fun `upgrade from github`() {
        val releaseTag = "v0.1.4-25"

        val (cluster, genesis) = initUpgradeCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                )
            )
        )
        genesis.markNeedsReboot()
        val pairs = cluster.joinPairs
        val height = genesis.getCurrentBlockHeight()
        val apiBinaries = upgradeArchitectures.associate { arch ->
            "linux/$arch" to getGithubPath(releaseTag, "decentralized-api-$arch.zip")
        }
        val binaries = upgradeArchitectures.associate { arch ->
            "linux/$arch" to getGithubPath(releaseTag, "inferenced-$arch.zip")
        }
        val upgradeBlock = height + 30
        Logger.info("Upgrade block: $upgradeBlock", "")
        logSection("Submitting upgrade proposal")
        val response = genesis.submitUpgradeProposal(
            title = releaseTag,
            description = "For testing",
            binaries = binaries,
            apiBinaries = apiBinaries,
            height = upgradeBlock,
            nodeVersion = "",
            deposit = 1000000,
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
        verifyUpgradeWithLocalApiRecovery(cluster, genesis)

    }
    @Test
    fun `submit upgrade`() {
        val (cluster, genesis) = initUpgradeCluster(
            config = inferenceConfig.copy(
                genesisSpec = createSpec(
                    epochLength = 100,
                    epochShift = 80
                )
            )
        )
        genesis.markNeedsReboot()
        val pairs = cluster.joinPairs
        waitForClusterOperational(cluster, genesis)
        assertLastUpgradeHeightUnset(cluster)
        val height = genesis.getCurrentBlockHeight()
        val binaries = getLocalUpgradeBinaries(
            baseDir = "v2/inferenced",
            archiveBaseName = "inferenced",
        )
        val apiBinaries = getLocalUpgradeBinaries(
            baseDir = "v2/dapi",
            archiveBaseName = "decentralized-api",
        )
        val upgradeBlock = height + 30
        Logger.info("Upgrade block: $upgradeBlock", "")
        logSection("Submitting upgrade proposal")
        val response = genesis.submitUpgradeProposal(
            title = "v0.0.1test",
            description = "For testing",
            binaries = binaries,
            apiBinaries = apiBinaries,
            height = upgradeBlock,
            nodeVersion = "",
            deposit = 1000000,
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
        assertLastUpgradeHeightUnset(cluster)
        logSection("Waiting for upgrade to finish")
        Thread.sleep(Duration.ofMinutes(5))
        logSection("Verifying upgrade")
        genesis.node.waitForNextBlock(1)
        verifyUpgradeWithLocalApiRecovery(cluster, genesis)
        waitForLastUpgradeHeight(cluster, genesis, upgradeBlock)
        assertLastUpgradeHeight(cluster, upgradeBlock)

    }


    // Classic inference flow removed (PR #1386). The MLNode versioned-endpoint
    // switching mechanism under test is still live (devshard calls ML nodes via
    // versioned segments too), but this test drives traffic through the removed
    // classic /v1/chat/completions path. TODO(devshard): rewrite the traffic
    // path through a devshard escrow so partial-upgrade endpoint switching has
    // end-to-end coverage again.
    @Test
    @Tag("exclude")
    @Timeout(value = 30, unit = TimeUnit.MINUTES)
    fun testVersionedEndpointSwitching() {
        val (cluster, genesis) = initUpgradeCluster()

        logSection("Waiting for initial system to be ready")
        var currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })

        // Test that the system works initially before we modify it
        logSection("Verifying system is working before version changes")
        genesis.waitForNextInferenceWindow()
        val systemCheckResponse = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(systemCheckResponse.choices.first().message.content).isNotEmpty()

        logSection("Setting up versioned mock responses")

        // Define unique responses for each version to clearly distinguish them
        val v038Response = "Response from version v3.0.8"
        val v039Response = "Response from version v3.0.9"
        val v0310Response = "Response from version v3.0.10"

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

        // Give governance enough runway to submit, deposit, and complete voting
        // before the target upgrade height is reached.
        val upgradeLeadBlocks = 30

        logSection("Initiating first upgrade: v3.0.8 → v3.0.9")
        val firstUpgradeHeight = genesis.getCurrentBlockHeight() + upgradeLeadBlocks

        val firstProposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = firstUpgradeHeight.toString(),
                nodeVersion = firstUpgradeVersion,
                apiBinariesJson = ""
            )
        )

        logSection("Waiting for first upgrade to take effect at height $firstUpgradeHeight")
        genesis.node.waitForMinimumBlock(firstUpgradeHeight + 1, "firstUpgradeHeight+1")

        logSection("Testing post-upgrade requests should hit v3.0.9 endpoints")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })
        val upgradedInferenceResponse = waitForInferenceReady(genesis, inferenceRequest)
        assertThat(upgradedInferenceResponse.choices.first().message.content)
            .withFailMessage("After first upgrade, inference should use v3.0.9 endpoint")
            .isEqualTo(v039Response)

        // Verify that the correct versioned URLs are being called
        logSection("Verifying v3.0.9 URLs are being used")
        cluster.allPairs.forEach { pair ->
            val hasV039Requests = pair.mock?.hasRequestsToVersionedEndpoint("v3.0.9") ?: false
            Logger.info("Node ${pair.name} received requests to v3.0.9 inference endpoints: $hasV039Requests", "")
            assertThat(hasV039Requests)
                .withFailMessage("Expected node ${pair.name} to receive requests on v3.0.9 inference endpoints")
                .isTrue()
        }

        logSection("Initiating second upgrade: v3.0.9 → v3.0.10")
        val secondUpgradeHeight = genesis.getCurrentBlockHeight() + upgradeLeadBlocks

        val secondProposalId = genesis.runProposal(
            cluster,
            CreatePartialUpgrade(
                height = secondUpgradeHeight.toString(),
                nodeVersion = secondUpgradeVersion,
                apiBinariesJson = ""
            )
        )

        logSection("Waiting for second upgrade to take effect at height $secondUpgradeHeight")
        genesis.node.waitForMinimumBlock(secondUpgradeHeight + 1, "secondUpgradeHeight+1")

        logSection("Testing post-second-upgrade requests should hit v3.0.10 endpoints")
        currentHeight = genesis.getCurrentBlockHeight()
        genesis.waitForBlock(5, { it.getCurrentBlockHeight() > (currentHeight + 3) })
        val finalInferenceResponse = waitForInferenceReady(genesis, inferenceRequest)
        assertThat(finalInferenceResponse.choices.first().message.content)
            .withFailMessage("After second upgrade, inference should use v3.0.10 endpoint")
            .isEqualTo(v0310Response)

        // Verify that the correct versioned URLs are being called
        logSection("Verifying v3.0.10 URLs are being used")
        cluster.allPairs.forEach { pair ->
            val hasV0310Requests = pair.mock?.hasRequestsToVersionedEndpoint("v3.0.10") ?: false
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

    private fun getGithubPath(releaseTag: String, fileName: String): String {
        val safeReleaseTag = URLEncoder.encode(releaseTag, "UTF-8")
        val path = "https://github.com/product-science/race-releases/releases/download/$safeReleaseTag/$fileName"
        val tempDir = File("downloads").apply { mkdirs() }
        val outputFile = File(tempDir, fileName)
        URL(path).openStream().use { input ->
            outputFile.outputStream().use { output ->
                input.copyTo(output)
            }
        }
        val sha = getSha256Checksum(outputFile.absolutePath)
        return "$path?checksum=sha256:$sha"
    }

    private fun getLocalUpgradeBinaries(
        baseDir: String,
        archiveBaseName: String,
    ): Map<String, String> {
        val binaries = upgradeArchitectures.mapNotNull { arch ->
            val relativePath = "$baseDir/$archiveBaseName-$arch.zip"
            val localFile = File("../public-html/$relativePath")
            if (localFile.exists()) {
                "linux/$arch" to getBinaryPath(relativePath)
            } else {
                null
            }
        }.toMap()

        require(binaries.isNotEmpty()) {
            "No local upgrade archives found for $archiveBaseName under ../public-html/$baseDir"
        }
        return binaries
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
