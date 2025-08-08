import com.productscience.*
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import com.productscience.getK8sInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequestObject
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.Logger
import java.io.File
import java.net.URL
import java.net.URLEncoder
import java.time.Duration
import java.util.concurrent.Executors
import kotlin.random.Random

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
    fun `spam interrupted requests`() {
        getK8sInferencePairs(inferenceConfig).use { k8pairs ->
            val maxConcurrentRequests = 100
            val totalRequests = 100
            val genesis = k8pairs.pairs.first { it.name == "genesis" }
            logSection("Making interrupted streaming inference")
            val limitedDispatcher = Executors.newFixedThreadPool(maxConcurrentRequests).asCoroutineDispatcher()

            runBlocking {
                val requests = List(totalRequests) { i ->
                    async(limitedDispatcher) {
                        org.tinylog.kotlin.Logger.info("Starting request $i")
                        makeInterruptedStreamingInferenceRequest(
                            genesis,
                            inferenceRequestStreamObject.toJson(),
                            Random.nextInt(80),
                            checkStarted = false
                        )
                    }
                }
                requests.awaitAll()
            }
            logSection("Waiting for next claim rewards")
            genesis.waitForStage(EpochStage.CLAIM_REWARDS)
            logSection("Recording aftermath")
            Thread.sleep(Duration.ofMinutes(20))

        }
    }
    @Test
    fun listenToK8s() {
        getK8sInferencePairs(inferenceConfig).use {
            Thread.sleep(Duration.ofMinutes(90))
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
    fun useApiLots() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            var successCount = 0
            var failureCount = 0

            repeat(20) { iteration ->
                try {
                    logSection("Iteration $iteration")
                    println("Iteration $iteration - Getting nodes...")
                    genesis.api.getNodes()
//                    val response = genesis.makeInferenceRequest(inferenceRequestObject.copy(model = "Qwen/Qwen2.5-7B-Instruct").toJson())
//                    println("INFERENCE: " + response.choices.first().message.content)
                    successCount++
                } catch (e: Exception) {
                    failureCount++
                    println("ERROR in iteration $iteration: ${e.message}")
                    e.printStackTrace()

                    // Add a longer delay after an error
                    Thread.sleep(2000)
                }
            }
            assertThat(failureCount).isEqualTo(0)
        }
    }

    @Test
    fun `record k8s activity`() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            Thread.sleep(Duration.ofHours(6))
        }
    }

    @Test
    fun k8sBasicInference() {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val response =
                genesis.makeInferenceRequest(inferenceRequestObject.copy(model = "Qwen/Qwen2.5-7B-Instruct").toJson())
            println("INFERENCE:" + response.choices.first().message.content)
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
    fun k8sManyRequests() = runBlocking {
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            val parameters = genesis.node.getInferenceParams()
            Logger.info("Parameters: $parameters", "")
            val response =
                genesis.makeInferenceRequest(inferenceRequestObject.copy(model = "Qwen/Qwen2.5-7B-Instruct").toJson())
            Logger.info("INFERENCE:" + response.choices.first().message.content, "")
            val inferences = runParallelInferences(genesis, 100, maxConcurrentRequests = 30, models = listOf("Qwen/Qwen2.5-7B-Instruct"))
            inferences.forEach { Logger.info(it) }
            Thread.sleep(Duration.ofMinutes(20))
        }
    }


    @Test
    fun k8sUpgrade() {
        val releaseTag = System.getenv("RELEASE_TAG") ?: "release/v0.1.4-25"
//        val releaseTag = "v0.1.4-28"
        val releaseVersion = releaseTag.substringAfterLast("/")
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
                title = releaseVersion,
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
        }
        // After 5 minutes and a reboot, we need to reconnect
        getK8sInferencePairs(inferenceConfig).use { k8sPairs ->
            val genesis = k8sPairs.first { it.name == "genesis" }
            logSection("Verifying upgrade")
            genesis.node.waitForNextBlock(1)
            // Some other action?
            k8sPairs.forEach {
                it.api.getParticipants()
                it.api.getNodes()
                it.node.getColdAddress()
            }
        }
    }
}

fun getGithubPath(releaseTag: String, fileName: String): String {
    val safeReleaseTag = URLEncoder.encode(releaseTag, "UTF-8")
    val path = "https://github.com/product-science/race-releases/releases/download/$safeReleaseTag/$fileName"
    val tempDir = File("downloads")
    downloadFile(path, fileName)
    val sha = getSha256Checksum(File(tempDir, fileName).absolutePath)
    return "$path?checksum=sha256:$sha"
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
