package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.api.command.CreateContainerResponse
import com.github.dockerjava.api.model.Bind
import com.github.dockerjava.api.model.ExposedPort
import com.github.dockerjava.api.model.HostConfig
import com.github.dockerjava.api.model.Ports
import com.github.dockerjava.api.model.Volume
import java.nio.file.Files
import java.nio.file.Path
import kotlin.io.path.ExperimentalPathApi
import kotlin.io.path.copyToRecursively
import kotlin.io.path.deleteRecursively

data class DockerGroup(
    val dockerClient: DockerClient,
    val keyName: String,
    val port: Int,
    val nodeConfigFile: String,
    val isGenesis: Boolean = false,
    val wiremockExternalPort: Int,
    val workingDirectory: String,
    val genesisGroup: DockerGroup? = null,
    val genesisOverridesFile: String,
    val publicUrl: String = "http://$keyName-api:8080",
    val pocCallbackUrl: String = publicUrl,
) {
    fun init() {
        tearDownExisting()
        setupFiles()
        val chain = createChainNodeContainer()
        val api = createApiContainer()
        val mock = createWiremockContainer()
        if (!dockerClient.listNetworksCmd().exec().any { it.name == "chain-public" })
            dockerClient.createNetworkCmd().withName("chain-public").exec()
        dockerClient.startContainerCmd(chain.id).exec()
        dockerClient.startContainerCmd(api.id).exec()
        dockerClient.startContainerCmd(mock.id).exec()
    }

    private fun tearDownExisting() {
        val containers = dockerClient.listContainersCmd().withShowAll(true).exec()
        val containersToStop = containers.filter {
            it.labels.any { (key, value) ->
                key == "com.docker.compose.project" && value == keyName
            }
        }
        containersToStop.forEach {
            println("Stopping container ${it.id} with name ${it.names.first()}")
            if (it.state == "running") {
                dockerClient.stopContainerCmd(it.id).exec()
            }
            dockerClient.removeContainerCmd(it.id).withForce(true).withRemoveVolumes(true).exec()
        }
    }

    private fun getCommonEnv(): List<String> {
        return listOfNotNull(
            "KEY_NAME=$keyName",
            "NODE_HOST=$keyName-node",
            "DAPI_API__POC_CALLBACK_URL=$pocCallbackUrl",
            "DAPI_API__PUBLIC_URL=$publicUrl",
            "DAP_CHAIN_NODE__IS_GENESIS=${isGenesis.toString().lowercase()}",
            "NODE_CONFIG_PATH=/root/node_config.json",
            "NODE_CONFIG=$nodeConfigFile",
            "PUBLIC_URL=$publicUrl",
            "PORT=$port",
            "POC_CALLBACK_URL=$publicUrl",
            "IS_GENESIS=${isGenesis.toString().lowercase()}",
            "WIREMOCK_PORT=$wiremockExternalPort",
            "GENESIS_OVERRIDES_FILE=$genesisOverridesFile",
            genesisGroup?.let { "SEED_NODE_RPC_URL=${it.rpcUrl}" },
            genesisGroup?.let { "SEED_NODE_P2P_URL=${it.p2pUrl}" },
            genesisGroup?.let { "DAPI_CHAIN_NODE__SEED_API_URL=${it.apiUrl}" },
        )
    }

    @OptIn(ExperimentalPathApi::class)
    private fun setupFiles() {
        val baseDir = Path.of(workingDirectory)
        if (isGenesis) {
            val prodLocal = baseDir.resolve("prod-local")
            prodLocal.deleteRecursively()
        }

        val mappingsDir = baseDir.resolve("prod-local/wiremock/$keyName/mappings")
        val filesDir = baseDir.resolve("prod-local/wiremock/$keyName/__files")
        val mappingsSourceDir = baseDir.resolve("testermint/src/main/resources/mappings")
        val publicHtmlDir = baseDir.resolve("public-html")

        Files.createDirectories(mappingsDir)
        Files.createDirectories(filesDir)

        mappingsSourceDir.copyToRecursively(mappingsDir, overwrite = true, followLinks = false)
        publicHtmlDir.copyToRecursively(filesDir, overwrite = true, followLinks = false)
    }

    val apiUrl = "http://$keyName-api:8080"
    val rpcUrl = "http://$keyName-node:26657"
    val p2pUrl = "http://$keyName-node:26656"

    init {
        require(isGenesis || genesisGroup != null) { "Genesis group must be provided" }
    }

    fun createApiContainer(): CreateContainerResponse {
        val containerName = "$keyName-api"
        val imageName = "gcr.io/decentralized-ai/api"

        val localBind = "$workingDirectory/prod-local/$keyName"
        val inferenceBind = Bind(localBind, Volume("/root/.inference"))
        val nodeConfigBind = Bind("$workingDirectory/$nodeConfigFile", Volume("/root/node_config.json"))

        val portBindings = Ports().apply {
            bind(ExposedPort.tcp(8080), Ports.Binding.bindPort(port))
        }
        val hostConfig = HostConfig()
            .withBinds(inferenceBind, nodeConfigBind)
            .withPortBindings(portBindings)
            .withNetworkMode("chain-public")

        return dockerClient.createContainerCmd(imageName)
            .withName(containerName)
            .withEnv(getCommonEnv())
            .withHostConfig(hostConfig)
            .withLabels(mapOf("com.docker.compose.project" to keyName))
            .exec()
    }

    fun createWiremockContainer(): CreateContainerResponse {
        val containerName = "$keyName-wiremock"

        val portBindings = Ports().apply {
            bind(ExposedPort.tcp(8080), Ports.Binding.bindPort(wiremockExternalPort))
        }
        val hostConfig = HostConfig()
            .withPortBindings(portBindings)
            .withBinds(Bind("$workingDirectory/prod-local/wiremock/$keyName", Volume("/home/wiremock")))
            .withNetworkMode("chain-public")

        // Create and start the container
        val container = dockerClient.createContainerCmd("wiremock/wiremock:latest")
            .withName(containerName)
            .withEntrypoint("/docker-entrypoint.sh", "--global-response-templating", "--disable-gzip", "--verbose")
            .withHostConfig(hostConfig)
            .withLabels(mapOf("com.docker.compose.project" to keyName))
            .exec()

        return container
    }

    // See https://chatgpt.com/share/6792cd0b-50a4-8008-8cac-01bcc6ff1566
    fun createChainNodeContainer(): CreateContainerResponse {
        val containerName = "$keyName-node"
        val imageName = "gcr.io/decentralized-ai/inferenced"
        val command = if (isGenesis) arrayOf("sh", "./init-docker-genesis.sh") else null

        // Define volume bindings
        val hostPath = "$workingDirectory/prod-local/$keyName"
        val containerPath = "/root/.inference"
        val bind = Bind(hostPath, Volume(containerPath))

        // Define port bindings
        val portBindings = Ports().apply {
            bind(ExposedPort.tcp(26656), Ports.Binding.bindPort(26656))
            bind(ExposedPort.tcp(26657), Ports.Binding.bindPort(26657))
        }

        // Configure HostConfig
        val hostConfig = HostConfig()
            .withBinds(bind)
            .withNetworkMode("chain-public")
            .apply { if (isGenesis) withPortBindings(portBindings) }

        val createCommand = dockerClient.createContainerCmd(imageName)
            .withName(containerName)
            .withEnv(getCommonEnv())
            .withHostConfig(hostConfig)
//            .withEntrypoint("/bin/sh")
            .withLabels(mapOf("com.docker.compose.project" to keyName))
            .apply { if (command != null) withCmd(*command) }

        // Create and start the container
        return createCommand.exec()
    }

}
