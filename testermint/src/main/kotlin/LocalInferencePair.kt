package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.api.model.*
import com.github.dockerjava.core.DockerClientBuilder
import com.github.kittinunf.fuel.core.FuelError
import com.productscience.data.*
import okhttp3.Address
import org.tinylog.kotlin.Logger
import java.io.File
import java.time.Duration
import java.time.Instant
import java.util.concurrent.ConcurrentHashMap

val nameExtractor = "(.+)-node".toRegex()

data class TestermintContainers(
    val nodes: List<Container>,
    val apis: List<Container>,
    val mocks: List<Container>,
    val config: ApplicationConfig
) {
    fun getApi(name: String): Container? = apis.find { it.names.any { it == "$name-api" || it == "/$name-api" } }
    fun getMock(name: String): Container? =
        mocks.find { it.names.any { it == "$name-mock-server" || it == "/$name-mock-server" } }

    fun getNode(name: String): Container? = nodes.find { it.names.any { it == "$name-node" || it == "/$name-node" } }
    fun getCli(name: String): ApplicationCLI? {
        val dockerClient = DockerClientBuilder.getInstance().build()
        val container = getNode(name) ?: return null
        val configWithName = config.copy(pairName = name)
        val nodeLogs = attachDockerLogs(dockerClient, name, "node", container.id)
        val executor = DockerExecutor(container.id, configWithName)
        return ApplicationCLI(configWithName, nodeLogs, executor, listOf())
    }
}

fun getRawContainers(config: ApplicationConfig): TestermintContainers {
    Logger.info("Getting local inference containers")
    val dockerClient = DockerClientBuilder.getInstance()
        .build()
    val containers = dockerClient.listContainersCmd().exec()
    Logger.info("Found ${containers.size} containers")
    containers.forEach {
        Logger.info("Container: ${it.names.first()} Status: ${it.state} Image: ${it.image} ID: ${it.id}")
    }
    val nodes: List<Container> =
        containers.filter { it.image == config.nodeImageName || it.image == config.genesisNodeImage }
    val apis = containers.filter { it.image == config.apiImageName }
    val mocks = containers.filter { it.image == config.mockImageName }
    return TestermintContainers(nodes, apis, mocks, config)
}

fun getLocalInferencePairs(config: ApplicationConfig): List<LocalInferencePair> {
    Logger.info("Getting local inference pairs")
    val dockerClient = DockerClientBuilder.getInstance()
        .build()
    val containers = dockerClient.listContainersCmd().exec()
    Logger.info("Found ${containers.size} containers")
    containers.forEach {
        Logger.info("Container: ${it.names.first()} Status: ${it.state} Image: ${it.image} ID: ${it.id}")
    }
    val nodes: List<Container> =
        containers.filter { it.image == config.nodeImageName || it.image == config.genesisNodeImage }
    val apis = containers.filter { it.image == config.apiImageName }
    val mocks = containers.filter { it.image == config.mockImageName }
    var foundPairs = 0
    if (nodes.size != apis.size) {
        Logger.error("Number of nodes (${nodes.size}) does not match number of APIs (${apis.size}). Tearing down containers")
        nodes.forEach{
            dockerClient.stopContainerCmd(it.id).exec()
            dockerClient.removeContainerCmd(it.id).exec()
        }
        apis.forEach{
            dockerClient.stopContainerCmd(it.id).exec()
            dockerClient.removeContainerCmd(it.id).exec()
        }
        throw InvalidClusterException("Number of nodes (${nodes.size}) does not match number of APIs (${apis.size})")
    }
    return nodes.mapNotNull { chainContainer ->
        foundPairs++
        val nameMatch = nameExtractor.find(chainContainer.names.first())
        if (nameMatch == null) {
            Logger.warn("Container does not match expected name format: ${chainContainer.names.first()}")
            return@mapNotNull null
        }
        val name = nameMatch.groupValues[1]
        val apiContainer: Container = apis.find { it.names.any { it == "$name-api" } } ?: throw InvalidClusterException(
            "Unable to find API container for $name"
        )
        // Find primary mock server
        val mockContainer: Container? = mocks.find { it.names.any { it == "$name-mock-server" } }
        
        // Find all mock servers for this participant (including numbered ones like mock-server-2, mock-server-3)
        val allMockContainers: List<Container> = mocks.filter { container ->
            container.names.any { containerName ->
                containerName == "$name-mock-server" || 
                containerName.matches(Regex("$name-mock-server-\\d+"))
            }
        }
        
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
        Logger.info("Found ${allMockContainers.size} mock servers for $name")
        allMockContainers.forEach { mockContainer ->
            Logger.info("  Mock: ${mockContainer.names.first()} on port ${mockContainer.getMappedPort(8080)}")
        }
        
        val executor = DockerExecutor(
            chainContainer.id,
            configWithName
        )
        val apiExecutor = DockerExecutor(
            apiContainer.id,
            configWithName
        )

        // Create mock objects for all discovered mock servers
        val mockObjects = allMockContainers.map { container ->
            MockServerInferenceMock(
                baseUrl = "http://localhost:${container.getMappedPort(8080)!!}", 
                name = container.names.first()
            )
        }

        LocalInferencePair(
            node = ApplicationCLI(configWithName, nodeLogs, executor, listOf()),
            api = ApplicationAPI(apiUrls, configWithName, dapiLogs, apiExecutor),
            mock = mockContainer?.let {
                MockServerInferenceMock(
                    baseUrl = "http://localhost:${it.getMappedPort(8080)!!}", name = it.names.first()
                )
            },
            name = name,
            config = configWithName,
            mocks = mockObjects
        )
    }
}

class InvalidClusterException(message: String) : RuntimeException(message)

private fun getUrlForPrivatePort(portMap: Map<Int?, ContainerPort>, privatePort: Int): String {
    val privateUrl = portMap[privatePort]?.ip?.takeUnless { it == "::" } ?: "localhost"
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
    val mock: IInferenceMock?, // Primary mock for backward compatibility
    val name: String,
    override val config: ApplicationConfig,
    var mostRecentParams: InferenceParams? = null,
    var mostRecentEpochData: EpochResponse? = null,
    val mocks: List<IInferenceMock> = emptyList(), // All mocks including primary
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

    /**
     * Sets PoC response on all mock servers for this participant.
     * This is useful for participants with multiple MLNodes (mock servers).
     */
    fun setPocResponseOnAllMocks(weight: Long, scenarioName: String = "ModelState") {
        mocks.forEach { it.setPocResponse(weight, scenarioName) }
    }

    /**
     * Sets PoC validation response on all mock servers for this participant.
     * This is useful for participants with multiple MLNodes (mock servers).
     */
    fun setPocValidationResponseOnAllMocks(weight: Long, scenarioName: String = "ModelState") {
        mocks.forEach { it.setPocValidationResponse(weight, scenarioName) }
    }

    fun getEpochLength(): Long {
        return this.mostRecentParams?.epochParams?.epochLength ?: this.getParams().epochParams.epochLength
    }

    fun refreshMostRecentState() {
        this.mostRecentEpochData = this.api.getLatestEpoch()
        this.mostRecentParams = this.node.getInferenceParams().params
    }

    fun getParams(): InferenceParams {
        refreshMostRecentState()
        return this.mostRecentParams ?: error("No inference params available")
    }

    fun getEpochData(): EpochResponse {
        refreshMostRecentState()
        return this.mostRecentEpochData ?: error("No epoch data available")
    }

    fun getBalance(address: String): Long {
        return this.node.getBalance(address, this.node.config.denom).balance.amount
    }

    fun queryCollateral(address: String): Collateral {
        return this.node.queryCollateral(address)
    }

    fun depositCollateral(amount: Long): TxResponse {
        return this.submitTransaction(
            listOf(
                "collateral",
                "deposit-collateral",
                "${amount}${this.config.denom}",
            )
        )
    }

    fun withdrawCollateral(amount: Long): TxResponse {
        return this.submitTransaction(
            listOf(
                "collateral",
                "withdraw-collateral",
                "${amount}${this.config.denom}",
            )
        )
    }

    fun makeInferenceRequest(
        request: String,
        account: String? = null,
        timestamp: Long = Instant.now().toEpochNanos(),
        taAddress: String = node.getColdAddress(),
    ): OpenAIResponse {
        val signature = node.signPayload(request, account, timestamp = timestamp, endpointAccount = taAddress)
        val address = node.getColdAddress()
        return api.makeInferenceRequest(request, address, signature, timestamp)
    }

    /**
     * Makes a streaming inference request that can be interrupted.
     *
     * @param request The request body as a string. The request should include "stream": true.
     * @param account The account to use for signing the payload (optional)
     * @return A StreamConnection object that can be used to read from the stream and interrupt it
     */
    fun streamInferenceRequest(request: String, account: String? = null): StreamConnection {
        // Ensure the request has the stream flag set to true
        val requestWithStream = if (!request.contains("\"stream\"")) {
            // If the request doesn't contain the stream flag, add it
            val lastBrace = request.lastIndexOf("}")
            if (lastBrace > 0) {
                val prefix = request.substring(0, lastBrace)
                val suffix = request.substring(lastBrace)
                val separator = if (prefix.trim().endsWith(",")) "" else ","
                "$prefix$separator\"stream\":true$suffix"
            } else {
                // If the request doesn't have a valid JSON structure, just use it as is
                request
            }
        } else if (!request.contains("\"stream\":true") && !request.contains("\"stream\": true")) {
            // If the request contains the stream flag but it's not set to true, set it to true
            request.replace("\"stream\":false", "\"stream\":true")
                .replace("\"stream\": false", "\"stream\": true")
        } else {
            // If the request already has the stream flag set to true, use it as is
            request
        }

        val address = node.getColdAddress()
        val timestamp = Instant.now().toEpochNanos()
        val signature = node.signPayload(requestWithStream, account, timestamp = timestamp, endpointAccount = address)
        return api.createInferenceStreamConnection(requestWithStream, address, signature, timestamp)
    }

    fun getCurrentBlockHeight(): Long {
        return node.getStatus().syncInfo.latestBlockHeight
    }

    fun changePoc(newPoc: Long, setNewValidatorsOffset: Int = 2) {
        this.mock?.setPocResponse(newPoc)
        this.waitForStage(EpochStage.START_OF_POC)
        // CometBFT validators have a 1 block delay
        this.waitForStage(EpochStage.SET_NEW_VALIDATORS, setNewValidatorsOffset)
    }

    data class WaitForStageResult(
        val stageBlock: Long,
        val stageBlockWithOffset: Long,
        val currentBlock: Long,
        val waitDuration: Duration,
    )

    fun waitForNextEpoch() {
        val epochData = getEpochData()
        logSection("Waiting for next epoch after epoch ${epochData.latestEpoch.index}")
        this.waitForStage(EpochStage.START_OF_POC)
        this.waitForStage(EpochStage.CLAIM_REWARDS)
        val newEpochData = getEpochData()
        logSection("Epoch is now ${newEpochData.latestEpoch.index}")
    }

    fun waitForNextInferenceWindow(windowSizeInBlocks: Int = 5): WaitForStageResult? {
        val epochData = getEpochData()
        val startOfNextPoc = epochData.getNextStage(EpochStage.START_OF_POC)
        val currentPhase = epochData.phase
        val currentBlockHeight = epochData.blockHeight
        Logger.info {
            "Checking if should wait for next SET_NEW_VALIDATORS to run inference. " +
                    "startOfNextPoc=$startOfNextPoc. " +
                    "currentBlockHeight=$currentBlockHeight. " +
                    "currentPhase=$currentPhase"
        }

        if (epochData.phase != EpochPhase.Inference ||
            startOfNextPoc - currentBlockHeight < windowSizeInBlocks
        ) {
            logSection("Waiting for CLAIM_REWARDS stage before running inference")
            return waitForStage(EpochStage.CLAIM_REWARDS)
        } else {
            Logger.info("Skipping wait for SET_NEW_VALIDATORS, current phase is ${epochData.phase}")
            return null
        }
    }

    fun waitForStage(stage: EpochStage, offset: Int = 1): WaitForStageResult {
        val stageBlock = getNextStage(stage)
        val stageBlockWithOffset = stageBlock + offset
        val waitStart = Instant.now()
        val currentBlock = this.node.waitForMinimumBlock(
            stageBlockWithOffset,
            "stage $stage" + if (offset > 0) "+$offset)" else ""
        )
        val waitEnd = Instant.now()

        return WaitForStageResult(
            stageBlock = stageBlock,
            stageBlockWithOffset = stageBlockWithOffset,
            currentBlock = currentBlock,
            waitDuration = Duration.between(waitStart, waitEnd),
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
            mostRecentEpochData = this.api.getLatestEpoch()
        }
        error("Block $targetBlock reached without condition passing")
    }

    fun getNextStage(stage: EpochStage): Long {
        val epochData = this.getEpochData()
        return epochData.getNextStage(stage)
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

    // FIXME: query this info from chain when epochs/0 endpoint is implemented?
    fun waitForFirstValidators() {
        if (this.mostRecentEpochData == null) {
            this.getParams()
        }

        val epochData = this.mostRecentEpochData
            ?: error("No epoch data available")

        if (epochData.epochParams.epochLength > 500) {
            error("Epoch length is too long for testing")
        }

        val epochParams = epochData.epochParams
        val epochFinished = epochParams.epochLength +
                epochParams.getStage(EpochStage.SET_NEW_VALIDATORS) +
                1 -
                epochParams.epochShift

        if (epochFinished <= epochData.blockHeight) {
            return
        }

        Logger.info("First PoC should be finished at block height $epochFinished")
        this.node.waitForMinimumBlock(epochFinished, "firstValidators")
    }

    fun submitMessage(message: TxMessage, waitForProcessed: Boolean = true): TxResponse =
        wrapLog("SubmitMessage", true) {
            submitTransaction(Transaction(TransactionBody(listOf(message), "", 0)), waitForProcessed)
        }

    fun submitTransaction(transaction: Transaction, waitForProcessed: Boolean = true): TxResponse =
        wrapLog("SubmitTransaction", true) {
            submitTransaction(cosmosJson.toJson(transaction), waitForProcessed)
        }

    fun waitForMlNodesToLoad(maxWaitAttempts: Int = 10, sleepTimeMillis: Long = 5_000L) {
        var i = 0
        while (true) {
            val nodes = api.getNodes()
            if (nodes.isNotEmpty() && nodes.all { n ->
                    n.state.currentStatus != "UNKNOWN" && n.state.intendedStatus != "UNKNOWN"
                }) {
                Logger.info("All nodes are loaded and ready. numNodes = ${nodes.size}. nodes = $nodes")
                break
            }

            i++
            if (i >= maxWaitAttempts) {
                error(
                    "Waited for ${sleepTimeMillis * 10} ms for ml node to be ready, but it never was." +
                            " Check if the mock server is running. pairName = ${name}. nodes = $nodes"
                )
            }

            Thread.sleep(sleepTimeMillis)
        }
    }


    fun submitTransaction(json: String, waitForProcessed: Boolean = true): TxResponse {
        val start = Instant.now()
        val submittedTransaction = try {
            this.api.submitTransaction(json)
        } catch (e: FuelError) {
            Logger.info("Checking for read timeout in " + e.toString())
            // We are seeing in k8s (remote) connections this timesout, even though the submit worked. This should pick
            // up the TXHash from the api logs instead.
            if (e.toString().contains("Read timed out")) {
                Logger.info(
                    "Found read timeout, checking node logs for TX hash in " +
                            this.api.logOutput.mostRecentTxResp
                )
                this.api.logOutput.mostRecentTxResp?.takeIf { it.time.isAfter(start) }?.let {
                    TxResponse(0, it.hash, "", 0, "", "", "", 0, 0, null, null, listOf())
                } ?: throw e
            } else {
                throw e
            }
        }
        return if (waitForProcessed && submittedTransaction.code == 0) {
            this.node.waitForTxProcessed(submittedTransaction.txhash)
        } else {
            submittedTransaction
        }
    }

    fun submitTransaction(args: List<String>, waitForProcessed: Boolean = true): TxResponse {
        val submittedTransaction = this.node.sendTransactionDirectly(args)
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
            val govAccount = this.node.getModuleAccount("gov")
            val govValue = govAccount.account.value as? AccountValue 
                ?: throw IllegalStateException("Gov module account value is not AccountValue")
            
            val finalProposal = proposal.copy(
                messages = proposal.messages.map {
                    it.withAuthority(govValue.address)
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
        binaries: Map<String, String>,
        apiBinaries: Map<String, String>,
        height: Long,
        nodeVersion: String,
        deposit: Int,
    ): TxResponse = wrapLog("submitUpgradeProposal", true) {
        // Convert maps to JSON format
        val binariesJsonObj = binaries.entries.joinToString(",") { (arch, path) -> "\"$arch\":\"$path\"" }
        val apiBinariesJsonObj = apiBinaries.entries.joinToString(",") { (arch, path) -> "\"$arch\":\"$path\"" }

        val binariesJson =
            """{"binaries":{$binariesJsonObj},"api_binaries":{$apiBinariesJsonObj}, "node_version": "$nodeVersion"}"""

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
                "${deposit}ngonka",
            )
        )
    }

    // Overloaded version for backward compatibility
    fun submitUpgradeProposal(
        title: String,
        description: String,
        binaryPath: String,
        apiBinaryPath: String,
        height: Long,
        nodeVersion: String,
    ): TxResponse = submitUpgradeProposal(
        title = title,
        description = description,
        binaries = mapOf("linux/amd64" to binaryPath),
        apiBinaries = mapOf("linux/amd64" to apiBinaryPath),
        height = height,
        nodeVersion = nodeVersion,
        deposit = 1000000
    )

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
            ).also {
                if (it.code != 0)
                    throw RuntimeException("Transaction failed: code=${it.code}, txhash=${it.txhash}, rawLog=${it.rawLog}")
            }.getProposalId()!!
            val response = this.makeGovernanceDeposit(proposalId, minDeposit)
            require(response.code == 0) { "Deposit failed: ${response.rawLog}" }
            val votingPeriodEnd = Instant.now().plus(govParams.votingPeriod)
            logSection("Voting on proposal, no voters: ${noVoters.joinToString(", ")}")
            cluster.allPairs.forEach {
                val voteResponse = it.voteOnProposal(proposalId, if (noVoters.contains(it.name)) "no" else "yes")
                require(voteResponse.code == 0) { "Vote failed: ${voteResponse.rawLog}" }
            }

            logSection("Waiting for voting period to end")
            while (Instant.now().isBefore(votingPeriodEnd)) {
                Thread.sleep(1000)
            }
            cluster.allPairs.first().node.waitForNextBlock(2)

            proposalId
        }

    fun makeGovernanceDeposit(proposalId: String, amount: Long): TxResponse = wrapLog("makeGovernanceDeposit", true) {
        this.submitTransaction(
            listOf(
                "gov",
                "deposit",
                proposalId,
                "$amount${config.denom}",
            )
        )
    }

    fun voteOnProposal(proposalId: String, option: String): TxResponse = wrapLog("voteOnProposal", true) {
        this.submitTransaction(
            listOf(
                "gov",
                "vote",
                proposalId,
                option,
            )
        )
    }

    fun markNeedsReboot() {
        File("reboot.txt").bufferedWriter().use { writer ->
            writer.write("true")
        }
    }

    fun waitForInference(inferenceId: String, finished: Boolean, blocks: Int = 5): InferencePayload? =
        wrapLog("waitForInference", true) {
            var inference: InferencePayload? = null
            var tries = 0
            while (tries < blocks &&
                (if (finished) inference?.actualCost == null else inference == null)
            ) {
                this.node.waitForNextBlock()
                inference = this.api.getInferenceOrNull(inferenceId)
                tries++
            }
            inference
        }
}

data class ApplicationConfig(
    val appName: String,
    val chainId: String,
    val nodeImageName: String,
    val genesisNodeImage: String,
    val apiImageName: String,
    val mockImageName: String,
    val denom: String,
    val stateDirName: String,
    val pairName: String = "",
    val genesisName: String = "genesis",
    val genesisSpec: Spec<AppState>? = null,
    // execName accommodates upgraded chains.
    val execName: String = "$stateDirName/cosmovisor/current/bin/$appName",
    val additionalDockerFilesByKeyName: Map<String, List<String>> = emptyMap(),
    val nodeConfigFileByKeyName: Map<String, String> = emptyMap(),
) {
    val mountDir: String
        get() = "./$chainId/$pairName:/root/$stateDirName"
    val keyringBackend: String
        get() = if (pairName.contains("genesis")) "test" else "file"
    val keychainParams: List<String>
        get() = listOf("--keyring-backend", keyringBackend, "--keyring-dir=/root/$stateDirName")
}

fun Instant.toEpochNanos(): Long {
    return this.epochSecond * 1_000_000_000 + this.nano.toLong()
}
