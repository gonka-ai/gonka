import com.github.kittinunf.fuel.core.FuelError
import com.google.gson.JsonObject
import com.google.gson.JsonParser
import com.productscience.*
import com.productscience.data.OpenAIResponse
import com.productscience.data.UpdateParams
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.Logger
import java.io.File
import java.security.MessageDigest
import java.time.Duration
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlin.test.assertNotNull

class UpgradeRehearsalTests : TestermintTest() {
    private val defaultDevshardMaxNonce = 20_000L

    @Test
    @Tag("upgrade-rehearsal")
    @Timeout(value = 90, unit = TimeUnit.MINUTES)
    fun `complete upgrade rehearsal`() {
        val targetUpgrade = requiredEnv("UPGRADE_REHEARSAL_TARGET")
        val manifest = loadPrepManifest()
        assertPreparedManifest(manifest)

        val cluster = attachPreparedCluster()
        val genesis = cluster.genesis
        configureInferenceMocks(cluster, targetUpgrade, "upgrade rehearsal post-upgrade")

        waitForClusterOperational(cluster, genesis)

        val upgradeLeadBlocks = System.getenv("UPGRADE_REHEARSAL_LEAD_BLOCKS")?.toLongOrNull() ?: 80L
        val upgradeHeight = genesis.getCurrentBlockHeight() + upgradeLeadBlocks
        val binaryPath = rehearsalBinaryPath("v2/inferenced/inferenced-amd64.zip")
        val apiBinaryPath = rehearsalBinaryPath("v2/dapi/decentralized-api-amd64.zip")

        logSection("Submitting upgrade rehearsal proposal for $targetUpgrade at block $upgradeHeight")
        val response = genesis.submitUpgradeProposal(
            title = targetUpgrade,
            description = "Testermint upgrade rehearsal to $targetUpgrade",
            binaryPath = binaryPath,
            apiBinaryPath = apiBinaryPath,
            height = upgradeHeight,
            nodeVersion = targetUpgrade,
        )
        require(response.code == 0) { "Upgrade proposal failed: ${response.rawLog}" }
        val proposalId = assertNotNull(response.getProposalId(), "could not find upgrade proposal id")

        val govParams = genesis.node.getGovParams().params
        val deposit = genesis.makeGovernanceDeposit(proposalId, govParams.minDeposit.first().amount)
        require(deposit.code == 0) { "Upgrade proposal deposit failed: ${deposit.rawLog}" }

        logSection("Voting on upgrade rehearsal proposal")
        cluster.allPairs.forEach { pair ->
            val vote = pair.voteOnProposal(proposalId, "yes")
            require(vote.code == 0) { "Vote failed for ${pair.name}: ${vote.rawLog}" }
        }

        logSection("Waiting for governance voting period")
        Thread.sleep(govParams.votingPeriod.plus(Duration.ofSeconds(5)).toMillis())

        logSection("Waiting for upgrade height $upgradeHeight")
        genesis.node.waitForMinimumBlock(upgradeHeight - 2, "upgrade rehearsal height")

        logSection("Waiting for cosmovisor to apply upgrade")
        Thread.sleep(Duration.ofMinutes(5).toMillis())
        genesis.node.waitForNextBlock(1)

        logSection("Verifying upgrade marker and cluster health")
        verifyUpgradeWithLocalApiRecovery(cluster, genesis)
        waitForLastUpgradeHeight(cluster, genesis, upgradeHeight, maxBlocks = 60)
        assertLastUpgradeHeight(cluster, upgradeHeight)
        waitForClusterOperational(cluster, genesis)

        logSection("Running post-upgrade normal inference")
        configureInferenceMocks(cluster, targetUpgrade, "upgrade rehearsal post-upgrade normal inference")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        waitForClusterOperational(cluster, genesis)
        genesis.waitForNextInferenceWindow()
        val postInference = makeInferenceRequestWhenRoutable(genesis, inferenceRequest)
        assertThat(postInference.choices.first().message.content).isNotEmpty()

        logSection("Running post-upgrade devshard settlement")
        ensureDevshardRequestsEnabled(cluster, genesis)
        configureDevshardMocks(cluster, targetUpgrade, "upgrade rehearsal post-upgrade devshard")
        val user = genesis.createFundedDevshardUser("upgrade-rehearsal-post-devshard-user")
        genesis.waitForNextInferenceWindow()
        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = defaultModel)
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName)
        try {
            genesis.waitForDevshardProxyWarmup()
            makeInferenceRequestWhenRoutable(genesis, inferenceRequest)
            repeat(5) { index ->
                val responseText = genesis.sendChatCompletion(
                    handle.proxyUrl,
                    defaultModel,
                    "upgrade rehearsal post-upgrade prompt $index",
                )
                assertThat(responseText).isNotEmpty()
            }
            genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = false,
            )
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }

        writeCompletionManifest(targetUpgrade, upgradeHeight, postInference.id, escrowId)
    }

    private fun ensureDevshardRequestsEnabled(cluster: LocalCluster, genesis: LocalInferencePair) {
        val params = genesis.getParams()
        val current = params.devshardEscrowParams?.devshardRequestsEnabled
        if (current == true) {
            logSection("Devshard request handling is already enabled")
            return
        }

        logSection("Enabling devshard request handling after upgrade through governance params")
        val devshardParams = requireNotNull(params.devshardEscrowParams) {
            "Cannot enable devshard request handling because devshard escrow params are missing"
        }
        genesis.runProposal(
            cluster,
            UpdateParams(
                params = params.copy(
                    devshardEscrowParams = devshardParams.copy(
                        devshardRequestsEnabled = true,
                        maxNonce = devshardParams.maxNonce.takeIf { it > 0 } ?: defaultDevshardMaxNonce,
                    ),
                ),
            ),
        )

        val updated = genesis.getParams().devshardEscrowParams?.devshardRequestsEnabled
        assertThat(updated).isEqualTo(true)
        waitForClusterOperational(cluster, genesis)
    }

    private fun attachPreparedCluster(): LocalCluster {
        val cluster = getLocalCluster(inferenceConfig)
            ?: error("No prepared Testermint cluster found. The rehearsal completion phase must not rebuild the cluster.")
        require(cluster.joinPairs.size >= 2) {
            "Expected at least two join pairs in prepared cluster, found ${cluster.joinPairs.size}"
        }
        return cluster
    }

    private fun assertPreparedManifest(manifest: JsonObject) {
        assertThat(manifest.get("phase").asString).isEqualTo("prepared")
        assertThat(manifest.get("normalInferenceId").asString).isNotBlank()
        assertThat(manifest.get("devshardEscrowId").asLong).isGreaterThan(0)
        assertThat(manifest.get("devshardRequests").asInt).isGreaterThan(0)
        assertThat(manifest.getAsJsonArray("participantIds")).isNotEmpty()
        assertThat(manifest.get("finalHeight").asLong).isGreaterThan(manifest.get("startingHeight").asLong)
    }

    private fun loadPrepManifest(): JsonObject {
        val file = manifestFile()
        require(file.exists()) { "Upgrade rehearsal prep manifest not found: ${file.absolutePath}" }
        return JsonParser.parseString(file.readText()).asJsonObject
    }

    private fun manifestFile(): File =
        System.getenv("UPGRADE_REHEARSAL_MANIFEST")
            ?.takeIf { it.isNotBlank() }
            ?.let(::File)
            ?: File("../prod-local/upgrade-rehearsal/manifest.json")

    private fun writeCompletionManifest(
        targetUpgrade: String,
        upgradeHeight: Long,
        postInferenceId: String,
        postDevshardEscrowId: Long,
    ) {
        val output = System.getenv("UPGRADE_REHEARSAL_COMPLETE_MANIFEST")
            ?.takeIf { it.isNotBlank() }
            ?.let(::File)
            ?: File(manifestFile().parentFile, "completion-manifest.json")
        output.parentFile.mkdirs()
        val manifest = mapOf(
            "schema" to 1,
            "phase" to "completed",
            "completedAt" to Instant.now().toString(),
            "targetUpgrade" to targetUpgrade,
            "upgradeHeight" to upgradeHeight,
            "postUpgradeInferenceId" to postInferenceId,
            "postUpgradeDevshardEscrowId" to postDevshardEscrowId,
        )
        output.writeText(cosmosJson.toJson(manifest))
        Logger.info("Wrote upgrade rehearsal completion manifest to {}", output.absolutePath)
    }

    private fun configureInferenceMocks(cluster: LocalCluster, targetUpgrade: String, content: String) {
        val response = defaultInferenceResponseObject.withResponse(content)
        cluster.allPairs.forEach { pair ->
            listOf("", "v3.0.8", targetUpgrade).distinct().forEach { segment ->
                pair.mock?.setInferenceResponse(response, segment = segment)
            }
        }
    }

    private fun configureDevshardMocks(cluster: LocalCluster, targetUpgrade: String, content: String) {
        val response = devshardChatResponse(content)
        cluster.allPairs.forEach { pair ->
            listOf("", "v3.0.8", targetUpgrade).distinct().forEach { segment ->
                pair.mock?.setInferenceResponse(response = response, segment = segment)
            }
        }
    }

    private fun devshardChatResponse(content: String): String =
        """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"$content"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}"""

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

    private fun assertLastUpgradeHeightUnset(cluster: LocalCluster) {
        assertLastUpgradeHeight(cluster, 0, expectedFound = false)
    }

    private fun verifyPairHealthy(pair: LocalInferencePair) {
        pair.api.getParticipants()
        pair.api.getNodes()
        pair.node.getColdAddress()
    }

    private fun makeInferenceRequestWhenRoutable(
        pair: LocalInferencePair,
        request: String,
        maxBlocks: Int = 25,
    ): OpenAIResponse {
        val startBlock = pair.getCurrentBlockHeight()
        val deadlineBlock = startBlock + maxBlocks
        var lastFailure: Throwable? = null

        while (pair.getCurrentBlockHeight() <= deadlineBlock) {
            try {
                return pair.makeInferenceRequest(request)
            } catch (e: Exception) {
                if (!isTransientInferenceRoutingFailure(e)) {
                    throw e
                }
                lastFailure = e
                Logger.warn(e) {
                    "Inference routing is not ready at block ${pair.getCurrentBlockHeight()}; waiting for next block"
                }
                pair.node.waitForNextBlock(1)
            }
        }

        throw IllegalStateException("Inference routing did not become ready by block $deadlineBlock", lastFailure)
    }

    private fun isTransientInferenceRoutingFailure(t: Throwable): Boolean {
        val transientMessages = listOf(
            "503",
            "Service Unavailable",
            "Active participants found, but length is 0",
            "After filtering participants the length is 0",
            "epoch group data not found",
        )
        val causeMatches = generateSequence(t) { it.cause }.any { cause ->
            transientMessages.any { message -> cause.message?.contains(message) == true }
        }
        val fuelMatches = generateSequence(t) { it.cause }.filterIsInstance<FuelError>().any { fuel ->
            fuel.response.statusCode == 503 ||
                transientMessages.any { message ->
                    fuel.response.data.toString(Charsets.UTF_8).contains(message)
                }
        }

        return causeMatches || fuelMatches
    }

    private fun pairIsOperational(pair: LocalInferencePair): Boolean =
        runCatching {
            verifyPairHealthy(pair)
            pair.api.getNodes().isNotEmpty() &&
                pair.api.getNodes().all { node ->
                    node.state.currentStatus != "UNKNOWN" && node.state.intendedStatus != "UNKNOWN"
                }
        }.getOrDefault(false)

    private fun waitForClusterOperational(cluster: LocalCluster, genesis: LocalInferencePair, maxBlocks: Int = 30) {
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

    private fun waitForLastUpgradeHeight(
        cluster: LocalCluster,
        genesis: LocalInferencePair,
        expectedHeight: Long,
        maxBlocks: Int,
    ) {
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
        fun pairHealthFailure(pair: LocalInferencePair): Throwable? =
            runCatching { verifyPairHealthy(pair) }.exceptionOrNull()

        fun failedPairs(): List<LocalInferencePair> =
            cluster.allPairs.filter { pair -> pairHealthFailure(pair) != null }

        val initialFailures = failedPairs()
        if (initialFailures.isEmpty()) {
            return
        }

        val firstFailure = pairHealthFailure(initialFailures.first())
        if (firstFailure == null || !isBadGatewayFailure(firstFailure)) {
            throw firstFailure ?: IllegalStateException("Upgrade verification failed for unknown reason")
        }

        Logger.warn(
            "Post-upgrade API verification failed with 502; restarting local API containers for {}",
            initialFailures.joinToString(", ") { it.name },
        )
        initialFailures.forEach { it.restartApiContainer() }

        val startBlock = genesis.getCurrentBlockHeight()
        val targetBlock = startBlock + 40
        while (genesis.getCurrentBlockHeight() < targetBlock) {
            val remainingFailures = failedPairs()
            if (remainingFailures.isEmpty()) {
                return
            }
            Logger.info(
                "Waiting for restarted API containers to recover: {}",
                remainingFailures.joinToString(", ") { it.name },
            )
            genesis.node.waitForNextBlock(2)
        }

        error("API containers did not recover from post-upgrade 502 by block $targetBlock")
    }

    private fun rehearsalBinaryPath(path: String): String {
        val localPath = File("../public-html/$path")
        val sha = sha256(localPath)
        val baseUrl = System.getenv("UPGRADE_BINARY_BASE_URL")
            ?.takeIf { it.isNotBlank() }
            ?: "http://genesis-mock-server:8080/files"
        return "${baseUrl.trimEnd('/')}/$path?checksum=sha256:$sha"
    }

    private fun sha256(file: File): String {
        require(file.exists()) { "Upgrade binary file does not exist: ${file.absolutePath}" }
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(8192)
            while (true) {
                val bytesRead = input.read(buffer)
                if (bytesRead == -1) {
                    break
                }
                digest.update(buffer, 0, bytesRead)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    private fun requiredEnv(name: String): String =
        System.getenv(name)?.takeIf { it.isNotBlank() }
            ?: error("Required environment variable $name is not set")
}
