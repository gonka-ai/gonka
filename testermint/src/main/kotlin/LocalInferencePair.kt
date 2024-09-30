package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.data.InferenceParticipant
import com.productscience.data.OpenAIResponse
import com.productscience.data.PubKey
import java.time.Instant

val nameExtractor = "inference-ignite-(.+)-node-1".toRegex()
fun getLocalInferencePairs(config: ApplicationConfig): List<LocalInferencePair> {
    val dockerClient = DockerClientBuilder.getInstance()
        .build()
    val containers = dockerClient.listContainersCmd().exec()
    val nodes = containers.filter { it.image == config.nodeImageName }
    val apis = containers.filter { it.image == config.apiImageName }
    return nodes.map {
        val name = nameExtractor.find(it.names.first())!!.groupValues[1]
        val matchingApi = apis.find { it.names.any { it.contains(name) } }!!
        val configWithName = config.copy(pairName = name)
        attachLogs(dockerClient, name, "node", it.id)
        attachLogs(dockerClient, name, "api", matchingApi.id)

        LocalInferencePair(
            ApplicationCLI(it.id, configWithName),
            ApplicationAPI("http://${matchingApi.ports[0].ip}:${matchingApi.ports[0].publicPort}", configWithName),
            name,
        )
    }
}

private fun attachLogs(
    dockerClient: DockerClient,
    name: String,
    type: String,
    id: String
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

    fun makeInferenceRequest(request: String): OpenAIResponse {
        val signature = node.signPayload(request)
        val address = node.getAddress()
        return api.makeInferenceRequest(request, address, signature)
    }

    fun getCurrentBlockHeight(): Long {
        return node.getStatus().syncInfo.latestBlockHeight
    }
}

data class ApplicationConfig(
    val appName: String,
    val chainId: String,
    val nodeImageName: String,
    val apiImageName: String,
    val denom: String,
    val stateDirName: String,
    val pairName: String = "",
)
