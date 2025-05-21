package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.api.model.*
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.data.*
import org.tinylog.kotlin.Logger
import java.io.File
import java.time.Instant
import java.util.concurrent.ConcurrentHashMap

val nameExtractor = "(.+)-node".toRegex()

fun getLocalInferencePairs(config: ApplicationConfig): List<LocalInferencePair> {
    Logger.info("Getting local inference pairs")
    val dockerClient = DockerClientBuilder.getInstance()
        .build()
    val containers = dockerClient.listContainersCmd().exec()
    Logger.info("Found ${containers.size} containers")
    containers.forEach {
        Logger.info("Container: ${it.names.first()} Status: ${it.state} Image: ${it.image} ID: ${it.id}")
    }
    val nodes = containers.filter { it.image == config.nodeImageName || it.image == config.genesisNodeImage }
    val apis = containers.filter { it.image == config.apiImageName }
    val mocks = containers.filter { it.image == config.wireMockImageName }
    var foundPairs = 0
    return nodes.mapNotNull { chainContainer ->
        foundPairs++
        val nameMatch = nameExtractor.find(chainContainer.names.first())
        if (nameMatch == null) {
            Logger.warn("Container does not match expected name format: ${chainContainer.names.first()}")
            return@mapNotNull null
        }
        val name = nameMatch.groupValues[1]
        val apiContainer: Container = apis.find { it.names.any { it == "$name-api" } }!!
        val mockContainer: Container? = mocks.find { it.names.any { it == "$name-wiremock" } }
        val configWithName = config.copy(pairName = name)
        val nodeLogs = attachDockerLogs(dockerClient, name, "node", chainContainer.id)
        val dapiLogs = attachDockerLogs(dockerClient, name, "dapi", apiContainer.id)

        val portMap = apiContainer.ports.associateBy { it.privatePort }
        Logger.info("Container ports: $portMap")
        val apiUrls = mapOf(
            SERVER_TYPE_PUBLIC to getUrlForPrivatePort(portMap, 9000),
            SERVER_TYPE_ML to getUrlForPrivatePort(portMap, 9100),
            SERVER_TYPE_ADMIN to getUrlForPrivatePort(portMap, 9200)
        )

        Logger.info("Creating local inference pair for $name")
        Logger.info("API URLs for ${apiContainer.names.first()}:")
        Logger.info("  $SERVER_TYPE_PUBLIC: ${apiUrls[SERVER_TYPE_PUBLIC]}")
        Logger.info("  $SERVER_TYPE_ML: ${apiUrls[SERVER_TYPE_ML]}")
        Logger.info("  $SERVER_TYPE_ADMIN: ${apiUrls[SERVER_TYPE_ADMIN]}")

        LocalInferencePair(
            node = ApplicationCLI(chainContainer.id, configWithName, nodeLogs),
            api = ApplicationAPI(apiUrls, configWithName, dapiLogs),
            mock = mockContainer?.let { InferenceMock(it.getMappedPort(8080)!!, it.names.first()) },
            name = name,
            config = configWithName
        )
    }
}

private fun getUrlForPrivatePort(portMap: Map<Int?, ContainerPort>, privatePort: Int): String {
    val privateUrl = portMap[privatePort]?.ip?.takeUnless { it == "::" } ?: "0.0.0.0"
    return "http://$privateUrl:${portMap[privatePort]?.publicPort}"
}

private fun Container.getMappedPort(internalPort: Int) =
    this.ports.find { it.privatePort == internalPort }?.publicPort

private fun DockerClient.getNodeId(
    config: ApplicationConfig,
) = createContainerCmd(config.nodeImageName)
    .withVolumes(Volume(config.mountDir))

private fun DockerClient.initNode(
    config: ApplicationConfig,
    isGenesis: Boolean = false,
) = executeCommand(
    config,
    """sh -c "chmod +x init-docker.sh; KEY_NAME=${config.pairName} IS_GENESIS=$isGenesis ./init-docker.sh""""
)

private fun DockerClient.executeCommand(
    config: ApplicationConfig,
    command: String,
) {
    val resp = createContainerCmd(config.nodeImageName)
        .withVolumes(Volume(config.mountDir))
        .withTty(true)
        .withStdinOpen(true)
        .withHostConfig(
            HostConfig()
                .withAutoRemove(true)
                .withLogConfig(LogConfig(LogConfig.LoggingType.LOCAL))
        )
        .withCmd(command)
        .exec()
    this.startContainerCmd(resp.id).exec()
}

//fun createLocalPair(config: ApplicationConfig, genesisPair: LocalInferencePair): LocalInferencePair {
//    val dockerClient = DockerClientBuilder.getInstance()
//        .build()
//
//}


private val attachedContainers = ConcurrentHashMap<String, LogOutput>()

fun attachDockerLogs(
    dockerClient: DockerClient,
    name: String,
    type: String,
    id: String,
): LogOutput {
    return attachedContainers.computeIfAbsent(id) { containerId ->
        val logOutput = LogOutput(name, type)
        dockerClient.logContainerCmd(containerId)
            .withSince(Instant.now().epochSecond.toInt())
            .withStdErr(true)
            .withStdOut(true)
            .withFollowStream(true)
            // Timestamps allow LogOutput to detect multi-line messages
            .withTimestamps(true)
            .exec(logOutput)
        logOutput
    }
}

data class LocalInferencePair(
    val node: ApplicationCLI,
    val api: ApplicationAPI,
    val mock: InferenceMock?,
    val name: String,
    override val config: ApplicationConfig,
    var mostRecentParams: InferenceParams? = null
) : HasConfig {
    fun addSelfAsParticipant(models: List<String>) {
        val status = node.getStatus()
        val validatorInfo = status.validatorInfo
        val pubKey: PubKey = validatorInfo.pubKey
        val self = InferenceParticipant(
            url = "http://$name-api:8080",
            models = models,
            validatorKey = pubKey.value
        )
        api.addInferenceParticipant(self)
    }

    fun getEpochLength(): Long {
        return this.mostRecentParams?.epochParams?.epochLength ?: this.getParams().epochParams.epochLength
    }

    fun getParams(): InferenceParams {
        this.mostRecentParams = this.node.getInferenceParams().params
        return this.mostRecentParams!!
    }

    fun makeInferenceRequest(request: String, account: String? = null): OpenAIResponse {
        val signature = node.signPayload(request, account)
        val address = node.getAddress()
        return api.makeInferenceRequest(request, address, signature)
    }

    fun getCurrentBlockHeight(): Long {
        return node.getStatus().syncInfo.latestBlockHeight
    }

    fun changePoc(newPoc: Long) {
        this.mock?.setPocResponse(newPoc)
        this.waitForStage(EpochStage.START_OF_POC)
        // CometBFT validators have a 1 block delay
        this.waitForStage(EpochStage.START_OF_POC, 2)
    }

    fun waitForStage(stage: EpochStage, offset: Int = 1) {
        this.node.waitForMinimumBlock(
            getNextStage(stage) + offset,
            "stage $stage" + if (offset > 0) "+$offset)" else ""
        )
    }

    fun waitForBlock(maxBlocks: Int, condition: (LocalInferencePair) -> Boolean) {
        val startBlock = this.getCurrentBlockHeight()
        var currentBlock = startBlock
        val targetBlock = startBlock + maxBlocks
        Logger.info("Waiting for block $targetBlock, current block $currentBlock to match condition")
        while (currentBlock < targetBlock) {
            if (condition(this)) {
                return
            }
            this.node.waitForNextBlock()
            currentBlock = this.getCurrentBlockHeight()
        }
        error("Block $targetBlock reached without condition passing")
    }

    fun getNextStage(stage: EpochStage): Long {
        if (this.mostRecentParams == null) {
            this.getParams()
        }
        val epochParams = this.mostRecentParams?.epochParams!!
        val currentHeight = this.getCurrentBlockHeight()
        val blocksTillEpoch = epochParams.epochLength - (currentHeight % epochParams.epochLength)
        val nextStage = currentHeight + blocksTillEpoch + epochParams.getStage(stage) + 1
        return if (nextStage - epochParams.epochLength > currentHeight)
            nextStage - epochParams.epochLength
        else nextStage
    }

    fun waitForFirstBlock() {
        while (this.mostRecentParams == null) {
            try {
                this.getParams()
            } catch (_: NotReadyException) {
                Logger.info("Node is not ready yet, waiting...")
                Thread.sleep(1000)
            }
        }
    }

    fun waitForFirstValidators() {
        if (this.mostRecentParams == null) {
            this.getParams()
        }
        val epochParams = this.mostRecentParams?.epochParams!!
        if (epochParams.epochLength > 500) {
            error("Epoch length is too long for testing")
        }
        val epochFinished = epochParams.epochLength +
                epochParams.getStage(EpochStage.SET_NEW_VALIDATORS) +
                1 -
                epochParams.epochShift
        Logger.info("First PoC should be finished at block height $epochFinished")
        this.node.waitForMinimumBlock(epochFinished, "firstValidators")
    }

    fun submitTransaction(args: List<String>, waitForProcessed: Boolean = true): TxResponse {
        val json = this.node.getTransactionJson(args)
        val submittedTransaction = this.api.submitTransaction(json)
        return if (waitForProcessed) {
            this.node.waitForTxProcessed(submittedTransaction.txhash)
        } else {
            submittedTransaction
        }
    }

    fun transferMoneyTo(destinationNode: ApplicationCLI, amount: Long): TxResponse = wrapLog("transferMoneyTo", true) {
        val sourceAccount = this.node.getKeys()[0].address
        val destAccount = destinationNode.getKeys()[0].address
        val response = this.submitTransaction(
            listOf(
                "bank",
                "send",
                sourceAccount,
                destAccount,
                "$amount${config.denom}",
            )
        )
        response
    }

    fun submitGovernanceProposal(proposal: GovernanceProposal): TxResponse =
        wrapLog("submitGovProposal", infoLevel = false) {
            val finalProposal = proposal.copy(
                messages = proposal.messages.map {
                    it.withAuthority(this.node.getModuleAccount("gov").account.value.address)
                },
            )
            val governanceJson = gsonCamelCase.toJson(finalProposal)
            val jsonFileName = "governance-proposal.json"
            node.writeFileToContainer(governanceJson, jsonFileName)

            this.submitTransaction(
                listOf(
                    "gov",
                    "submit-proposal",
                    jsonFileName
                )
            )
        }

    fun submitUpgradeProposal(
        title: String,
        description: String,
        binaryPath: String,
        apiBinaryPath: String,
        height: Long,
        nodeVersion: String,
    ): TxResponse = wrapLog("submitUpgradeProposal", true) {
        val proposer = this.node.getKeys()[0].address
        val binariesJson =
            """{"binaries":{"linux/amd64":"$binaryPath"},"api_binaries":{"linux/amd64":"$apiBinaryPath"}, "node_version": "$nodeVersion"}"""
        this.submitTransaction(
            listOf(
                "upgrade",
                "software-upgrade",
                title,
                "--title",
                title,
                "--upgrade-height",
                "$height",
                "--upgrade-info",
                binariesJson,
                "--summary",
                description,
                "--deposit",
                // TODO: Denom and amount should not be hardcoded
                "100000nicoin",
                "--from",
                proposer,
            )
        )
    }

    fun runProposal(cluster: LocalCluster, proposal: GovernanceMessage, noVoters: List<String> = emptyList()): String =
        wrapLog("runProposal", true) {
            logSection("Submitting and funding proposal")
            val govParams = this.node.getGovParams().params
            val minDeposit = govParams.minDeposit.first().amount
            val proposalId = this.submitGovernanceProposal(
                GovernanceProposal(
                    metadata = "http://www.yahoo.com",
                    deposit = "${minDeposit}${inferenceConfig.denom}",
                    title = "Extend the expiration blocks",
                    summary = "some inferences are taking a very long time to respond to, we need a longer expiration",
                    expedited = false,
                    messages = listOf(
                        proposal
                    )
                )
            ).getProposalId()!!
            this.makeGovernanceDeposit(proposalId, minDeposit)
            logSection("Voting on proposal, no voters: ${noVoters.joinToString(", ")}")
            cluster.allPairs.forEach {
                it.voteOnProposal(proposalId, if (noVoters.contains(it.name)) "no" else "yes")
            }
            proposalId
        }

    fun makeGovernanceDeposit(proposalId: String, amount: Long): TxResponse = wrapLog("makeGovernanceDeposit", true) {
        val depositor = this.node.getKeys()[0].address
        this.submitTransaction(
            listOf(
                "gov",
                "deposit",
                proposalId,
                "$amount${config.denom}",
                "--from",
                depositor,
            )
        )
    }

    fun voteOnProposal(proposalId: String, option: String): TxResponse = wrapLog("voteOnProposal", true) {
        val voter = this.node.getKeys()[0].address
        this.submitTransaction(
            listOf(
                "gov",
                "vote",
                proposalId,
                option,
                "--from",
                voter,
            )
        )
    }

    fun markNeedsReboot() {
        File("reboot.txt").bufferedWriter().use { writer ->
            writer.write("true")
        }
    }

}

data class ApplicationConfig(
    val appName: String,
    val chainId: String,
    val nodeImageName: String,
    val genesisNodeImage: String,
    val apiImageName: String,
    val wireMockImageName: String,
    val denom: String,
    val stateDirName: String,
    val pairName: String = "",
    val genesisName: String = "genesis",
    val genesisSpec: Spec<AppState>? = null,
    // execName accommodates upgraded chains.
    val execName: String = "$stateDirName/cosmovisor/current/bin/$appName"
) {
    val mountDir = "./$chainId/$pairName:/root/$stateDirName"
    val keychainParams = listOf("--keyring-backend", "test", "--keyring-dir=/root/$stateDirName")
}
