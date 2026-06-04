package com.productscience

import com.google.gson.Gson
import com.google.gson.JsonParser
import com.google.gson.annotations.SerializedName
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
import java.util.concurrent.TimeUnit

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
    val proxies = containers.filter { it.names.any { name -> name.endsWith("-proxy") } }
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
        val proxyContainer: Container = proxies.find { it.names.any { it == "$name-proxy" } }
            ?: return@mapNotNull null // Pair not fully up yet, skip
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
        val proxyPortMap = proxyContainer.ports.associateBy { it.privatePort }
        Logger.info("API container ports: $portMap")
        Logger.info("Proxy container ports: $proxyPortMap")

        val apiUrls = mapOf(
            SERVER_TYPE_PUBLIC to getUrlForPrivatePort(proxyPortMap, 80),
            SERVER_TYPE_ML to getUrlForPrivatePort(portMap, 9100),
            SERVER_TYPE_ADMIN to getUrlForPrivatePort(portMap, 9200)
        )
        val nodeManagerGrpcHostPort = portMap[9400]?.publicPort

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
            nodeManagerGrpcHostPort = nodeManagerGrpcHostPort,
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

data class VersiondInstallMetadata(
    @SerializedName("archive_sha256")
    val archiveSha256: String,
    @SerializedName("binary_sha256")
    val binarySha256: String,
)

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

// Admin bearer the test proxies start with. A fresh single-escrow gateway has no
// configured model access, so the gateway 401s normal traffic; sending this admin
// key bypasses model-access control (see modelAccessError in devshardctl).
const val devshardAdminApiKey = "sk-admin-test"

data class LocalInferencePair(
    val node: ApplicationCLI,
    val api: ApplicationAPI,
    val mock: IInferenceMock?, // Primary mock for backward compatibility
    val name: String,
    override val config: ApplicationConfig,
    val nodeManagerGrpcHostPort: Int? = null,
    var mostRecentParams: InferenceParams? = null,
    var mostRecentEpochData: EpochResponse? = null,
) : HasConfig {
    /**
     * Gets an alternative API URL using DNS alias (api.{name}.test).
     * This URL:
     * 1. Is different from the default container name (for URL change tests)
     * 2. Passes SSRF validation (not a private IP)
     * 3. Resolves via CoreDNS to the same API container
     */
    fun getAlternativeApiUrl(): String {
        val cleanName = name.trimStart('/')
        return "http://api.$cleanName.test:9000"
    }

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

    fun stopApiContainer() {
        val apiContainer = getRawContainers(config).getApi(name)
            ?: error("API container not found for $name")
        DockerClientBuilder.getInstance().build().use { dockerClient ->
            dockerClient.stopContainerCmd(apiContainer.id).exec()
        }
    }

    private fun siblingContainerId(serviceName: String): String {
        val cleanName = name.trimStart('/')
        val expectedNames = setOf("$cleanName-$serviceName", "/$cleanName-$serviceName")
        DockerClientBuilder.getInstance().build().use { dockerClient ->
            return dockerClient.listContainersCmd()
                .withShowAll(true)
                .exec()
                .find { container -> container.names.any { it in expectedNames } }
                ?.id
                ?: error("Container not found for $cleanName service=$serviceName")
        }
    }

    fun execInVersiond(args: List<String>, stdin: String? = null): List<String> = wrapLog("execInVersiond", false) {
        DockerExecutor(siblingContainerId("versiond"), config).exec(args, stdin)
    }

    fun curlFromApiNetwork(url: String): String = wrapLog("curlFromApiNetwork", false) {
        api.executor.exec(listOf("sh", "-c", "curl -sf '$url'"), null).joinToString("").trim()
    }

    fun versiondBinaryPath(versionName: String, binaryName: String = "devshardd"): String =
        "/opt/versiond/bin/$versionName/$binaryName"

    fun versiondInstallMetadataPath(versionName: String): String =
        "/opt/versiond/bin/$versionName/install.json"

    fun versiondBinaryExists(versionName: String, binaryName: String = "devshardd"): Boolean =
        try {
            execInVersiond(
                listOf("sh", "-c", "test -x '${versiondBinaryPath(versionName, binaryName)}' && echo OK"),
                null,
            ).any { it.contains("OK") }
        } catch (_: Exception) {
            false
        }

    fun readVersiondInstallMetadata(versionName: String): VersiondInstallMetadata? =
        try {
            val installPath = versiondInstallMetadataPath(versionName)
            val json = execInVersiond(
                listOf("sh", "-c", "test -f '$installPath' && cat '$installPath'"),
                null,
            ).joinToString("").trim()
            json.takeIf { it.isNotBlank() }?.let {
                cosmosJson.fromJson(it, VersiondInstallMetadata::class.java)
            }
        } catch (_: Exception) {
            null
        }

    fun readVersiondLogs(tail: Int = 200): String = wrapLog("readVersiondLogs", false) {
        val output = ExecCaptureOutput()
        val containerId = siblingContainerId("versiond")
        DockerClientBuilder.getInstance().build().use { dockerClient ->
            val command = dockerClient.logContainerCmd(containerId)
                .withStdOut(true)
                .withStdErr(true)
                .withTimestamps(false)
            if (tail > 0) {
                command.withTail(tail)
            }
            val callback = command.exec(output)
            callback.awaitCompletion(10, TimeUnit.SECONDS)
            callback.close()
        }
        output.output.joinToString("")
    }

    fun setPocWeight(weight: Long, node: InferenceNode? = null) {
        if (node == null) {
            this.api.getNodes().forEach {
                this.mock?.setPocResponse(weight, it.node.pocHost)
            }
        } else {
            this.mock?.setPocResponse(weight, node.pocHost)
        }
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

    fun createDevshardEscrow(amount: Long, modelId: String): TxResponse {
        return this.submitTransaction(
            listOf("inference", "create-devshard-escrow", amount.toString(), modelId)
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
        // Phase 3: Use signRequest to auto-hash the request
        val signature = node.signRequest(request, account, timestamp = timestamp, endpointAccount = taAddress)
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
        // Phase 3: Use signRequest to auto-hash the request
        val signature = node.signRequest(requestWithStream, account, timestamp = timestamp, endpointAccount = address)
        return api.createInferenceStreamConnection(requestWithStream, address, signature, timestamp)
    }

    fun getCurrentBlockHeight(): Long {
        return node.getStatus().syncInfo.latestBlockHeight
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
            this.node.waitForNextBlock(2)
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

    fun addNodes(nodesToAdd: Int): List<InferenceNode> {
        val nodes = (1..nodesToAdd).map { i ->
            validNode.copy(
                id = "multinode$i",
                host = hostName(i, this)
            )
        }

        return this.api.addNodes(nodes)
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

    fun submitTransactionWithFees(args: List<String>, fees: String, waitForProcessed: Boolean = true): TxResponse {
        val submittedTransaction = this.node.sendTransactionWithFees(args, fees)
        return if (waitForProcessed && submittedTransaction.code == 0) {
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

    data class DevshardProxyHandle(val escrowId: Long, val port: Int, val proxyUrl: String)

    fun startDevshardProxy(
        escrowId: Long,
        keyName: String? = null,
        port: Int = 18080 + escrowId.toInt(),
        routePrefix: String? = null,
        model: String = defaultModel,
    ): DevshardProxyHandle =
        wrapLog("startDevshardProxy", true) {
            val privateKey = (if (keyName != null) node.getPrivateKey(keyName) else node.getColdPrivateKey()).trim()
            val stderrFile = "/tmp/devshardctl-proxy-${escrowId}.log"
            // Tests pin the route prefix explicitly so they are not coupled to
            // devshardctl's release-default routing choice.
            val effectiveRoutePrefix = routePrefix ?: "/v1/devshard"
            val routePrefixEnv = " DEVSHARD_ROUTE_PREFIX='$effectiveRoutePrefix'"
            val startCommand = listOf(
                "sh", "-c",
                "DEVSHARD_PRIVATE_KEY='$privateKey'" +
                    " DEVSHARD_ESCROW_ID=$escrowId" +
                    " DEVSHARD_MODEL='$model'" +
                    " DEVSHARD_ADMIN_API_KEY='$devshardAdminApiKey'" +
                    " DEVSHARD_CHAIN_REST=http://\$NODE_HOST:1317" +
                    " DEVSHARD_PORT=$port" +
                    // Lift gateway rate limits for tests. The dynamic cap is
                    // floor(weight * per10000 / 10000); tiny test PoC weight rounds it to 0.
                    // Per10000=0 does not disable it -- WithTuningDefaults resets 0 to the
                    // default 5/10 -- so set it high instead to keep the cap large.
                    " GATEWAY_MAX_CONCURRENT_REQUESTS=0" +
                    " GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT=0" +
                    " GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT=1000000" +
                    " GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT=1000000" +
                    // Isolate each escrow's gateway store in its own dir. The gateway
                    // bootstraps DEVSHARD_ESCROW_ID from env only when no gateway.db
                    // exists in the base dir; a shared base dir makes a second escrow's
                    // proxy load the first escrow's persisted state instead.
                    " DEVSHARD_STORAGE_DIR=/tmp/devshardctl-proxy-${escrowId}" +
                    routePrefixEnv +
                    " nohup devshardctl >$stderrFile 2>&1 &" +
                    " echo \$!"
            )
            api.executor.exec(startCommand, null)
            // Wait for proxy to be ready.
            val proxyUrl = "http://localhost:$port"
            var ready = false
            for (i in 0 until 30) {
                try {
                    val output = api.executor.exec(listOf("sh", "-c", "curl -sf $proxyUrl/v1/status >/dev/null 2>&1 && echo OK"), null)
                    if (output.any { it.trim() == "OK" }) {
                        ready = true
                        break
                    }
                } catch (_: Exception) { }
                Thread.sleep(500)
            }
            if (!ready) {
                val logs = try {
                    api.executor.exec(listOf("cat", stderrFile), null).joinToString("")
                } catch (_: Exception) { "no logs" }
                error("devshardctl did not start within 15s. Logs:\n$logs")
            }
            DevshardProxyHandle(escrowId, port, proxyUrl)
        }

    fun stopDevshardProxy(escrowId: Long) {
        try {
            api.executor.exec(listOf("sh", "-c", "pkill -f 'DEVSHARD_ESCROW_ID=$escrowId.*devshardctl' || true"), null)
        } catch (_: Exception) { /* ignore */ }
    }

    // Returns every inference the gateway knows about, keyed by inference id.
    // Uses /v1/state (the per-runtime full state snapshot) which carries status and
    // votes per inference. Replaces the removed /v1/inference per-id endpoint.
    fun getDevshardProxyInferences(proxyUrl: String): Map<Long, DevshardInferencePayload> {
        val raw = api.executor.exec(listOf(
            "sh", "-c",
            "curl -sf $proxyUrl/v1/state -H 'Authorization: Bearer $devshardAdminApiKey'"
        ), null).joinToString("")
        val start = raw.indexOf('{')
        val end = raw.lastIndexOf('}')
        if (start < 0 || end < 0) {
            error("state returned no JSON object. raw:\n$raw")
        }
        val root = JsonParser.parseString(raw.substring(start, end + 1)).asJsonObject
        val inferences = root.getAsJsonObject("inferences") ?: return emptyMap()
        return inferences.entrySet().associate { (id, value) ->
            id.toLong() to cosmosJson.fromJson(value, DevshardInferencePayload::class.java)
        }
    }

    fun sendChatCompletion(proxyUrl: String, model: String, prompt: String, stream: Boolean = false): String {
        val body = """{"model":"$model","messages":[{"role":"user","content":"$prompt"}],"max_tokens":100,"stream":$stream}"""
        val maxTimeSeconds = if (stream) 55 else 30
        val result = api.executor.exec(listOf(
            "sh", "-c",
            "curl --silent --show-error --fail --connect-timeout 5 --max-time $maxTimeSeconds " +
                "-X POST $proxyUrl/v1/chat/completions " +
                "-H 'Content-Type: application/json' " +
                "-H 'Authorization: Bearer $devshardAdminApiKey' " +
                "-d '${body.replace("'", "'\\''")}'"
        ), null)
        return result.joinToString("")
    }

    fun getDevshardProxyStatus(proxyUrl: String): DevshardProxyStatus {
        val raw = api.executor.exec(listOf(
            "sh", "-c",
            "curl --silent --show-error --fail $proxyUrl/v1/status"
        ), null).joinToString("")
        val start = raw.indexOf('{')
        val end = raw.lastIndexOf('}')
        if (start < 0 || end < 0) {
            error("status returned no JSON object. raw:\n$raw")
        }
        val json = raw.substring(start, end + 1)
        return Gson().fromJson(json, DevshardProxyStatus::class.java)
    }

    fun finalizeDevshardProxy(proxyUrl: String): DevshardctlResult {
        val raw = api.executor.exec(listOf(
            "sh", "-c",
            "curl -sf -X POST $proxyUrl/v1/finalize -H 'Authorization: Bearer $devshardAdminApiKey'"
        ), null).joinToString("")
        val start = raw.indexOf('{')
        val end = raw.lastIndexOf('}')
        if (start < 0 || end < 0) {
            error("finalize returned no JSON object. raw:\n$raw")
        }
        val json = raw.substring(start, end + 1)
        val parsed = Gson().fromJson(json, DevshardSettlementData::class.java)
        return DevshardctlResult(parsed = parsed, rawJson = json, stderr = "")
    }

    data class DevshardctlResult(val parsed: DevshardSettlementData, val rawJson: String, val stderr: String)

    fun settleDevshardEscrow(settlementJson: String, from: String? = null): TxResponse =
        wrapLog("settleDevshardEscrow", true) {
            node.writeFileToContainer(settlementJson, "settlement.json")
            if (from != null) {
                val txResp = node.sendTransactionDirectly(
                    listOf("inference", "settle-devshard-escrow", "settlement.json"),
                    from
                )
                node.waitForTxProcessed(txResp.txhash)
            } else {
                submitTransaction(listOf("inference", "settle-devshard-escrow", "settlement.json"))
            }
        }

    fun createDevshardEscrow(amount: Long, from: String, modelId: String = defaultModel): TxResponse {
        val txResp = node.sendTransactionDirectly(
            listOf("inference", "create-devshard-escrow", amount.toString(), modelId),
            from
        )
        return node.waitForTxProcessed(txResp.txhash)
    }

    fun waitForInference(inferenceId: String, finished: Boolean, blocks: Int = 5): InferencePayload? =
        wrapLog("waitForInference", true) {
            var inference: InferencePayload? = null
            var tries = 0
            while (tries < blocks &&
                (if (finished) inference?.actualCost == null else inference == null)
            ) {
                this.node.waitForNextBlock(2)
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
    // Extra env vars passed to docker compose for every pair. Used by tests
    // that need to enable optional features (e.g. running devshardd under
    // versiond) without polluting the JVM-wide environment.
    val additionalEnvVars: Map<String, String> = emptyMap(),
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

private fun hostName(i: Int, participant: LocalInferencePair) = "ml-${String.format("%04d", i)}.${participant.name.trimStart('/')}.test"
