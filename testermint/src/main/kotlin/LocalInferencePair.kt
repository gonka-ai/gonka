package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.api.model.Container
import com.github.dockerjava.api.model.HostConfig
import com.github.dockerjava.api.model.LogConfig
import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.data.AppExport
import com.productscience.data.InferenceParticipant
import com.productscience.data.OpenAIResponse
import com.productscience.data.PubKey
import org.tinylog.kotlin.Logger
import java.time.Instant

val nameExtractor = "(.+)-node".toRegex()
fun getLocalInferencePairs(config: ApplicationConfig): List<LocalInferencePair> {
    val dockerClient = DockerClientBuilder.getInstance()
        .build()
    val containers = dockerClient.listContainersCmd().exec()
    val nodes = containers.filter { it.image == config.nodeImageName || it.image == config.genesisNodeImage }
    val apis = containers.filter { it.image == config.apiImageName }
    val mocks = containers.filter { it.image == config.wireMockImageName }
    return nodes.mapNotNull {
        val nameMatch = nameExtractor.find(it.names.first())
        if (nameMatch == null) {
            Logger.warn("Container does not match expected name format: ${it.names.first()}")
            return@mapNotNull null
        }
        val name = nameMatch.groupValues[1]
        val matchingApi = apis.find { it.names.any { it == "$name-api" } }!!
        val matchingMock: Container? = mocks.find { it.names.any { it == "$name-wiremock" } }
        val configWithName = config.copy(pairName = name)
        attachLogs(dockerClient, name, "node", it.id)
        attachLogs(dockerClient, name, "api", matchingApi.id)

        LocalInferencePair(
            ApplicationCLI(it.id, configWithName),
            ApplicationAPI("http://${matchingApi.ports[0].ip}:${matchingApi.ports[0].publicPort}", configWithName),
            matchingMock?.let { InferenceMock(it.getMappedPort(8080)!!, it.names.first()) },
            name
        )
    }
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

private fun attachLogs(
    dockerClient: DockerClient,
    name: String,
    type: String,
    id: String,
) {
    dockerClient.logContainerCmd(id)
        .withSince(Instant.now().epochSecond.toInt())
        .withStdErr(true)
        .withStdOut(true)
        .withFollowStream(true)
        // Timestamps allow LogOutput to detect multi-line messages
        .withTimestamps(true)
        .exec(LogOutput(name, type))
}

data class LocalInferencePair(
    val node: ApplicationCLI,
    val api: ApplicationAPI,
    val mock: InferenceMock?,
    val name: String,
) {
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

    fun makeInferenceRequest(request: String, account: String? = null): OpenAIResponse {
        val signature = node.signPayload(request, account)
        val address = node.getAddress()
        return api.makeInferenceRequest(request, address, signature)
    }

    fun getCurrentBlockHeight(): Long {
        return node.getStatus().syncInfo.latestBlockHeight
    }

    fun getNextSettleBlock(): Long {
        val epochParams = this.node.mostRecentExport?.appState?.inference?.params?.epochParams ?: return 0
        val currentHeight = this.getCurrentBlockHeight()
        val blocksTillEpoch = epochParams.epochLength - (currentHeight % epochParams.epochLength)
        val nextSettle = currentHeight + blocksTillEpoch + epochParams.getSetNewValidatorsStage() + 1
        return if (nextSettle - epochParams.epochLength > currentHeight)
            nextSettle - epochParams.epochLength
        else
            nextSettle
    }

    fun waitForFirstPoC() {
        val epochParams = this.node.mostRecentExport?.appState?.inference?.params?.epochParams ?: return
        val epochFinished = epochParams.epochLength + epochParams.getSetNewValidatorsStage() + 1
        Logger.info("First PoC should be finished at block height $epochFinished")
        this.node.waitForMinimumBlock(epochFinished)
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
) {
    val mountDir = "./$chainId/$pairName:/root/$stateDirName"
    val keychainParams = listOf("--keyring-backend", "test", "--keyring-dir=/root/$stateDirName")
}
