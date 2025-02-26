package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.Consumer.Companion.create
import com.productscience.data.UnfundedInferenceParticipant
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption
import kotlin.contracts.ExperimentalContracts
import kotlin.contracts.contract
import kotlin.io.path.ExperimentalPathApi
import kotlin.io.path.copyToRecursively
import kotlin.io.path.deleteRecursively

const val GENESIS_COMPOSE_FILE = "docker-compose-local-genesis.yml"
const val NODE_COMPOSE_FILE = "docker-compose-local.yml"

data class GenesisUrls(val keyName: String) {
    val apiUrl = "http://$keyName-api:8080"
    val rpcUrl = "http://$keyName-node:26657"
    val p2pUrl = "http://$keyName-node:26656"
}

data class DockerGroup(
    val dockerClient: DockerClient,
    val keyName: String,
    val port: Int,
    val nodeConfigFile: String,
    val isGenesis: Boolean = false,
    val wiremockExternalPort: Int,
    val workingDirectory: String,
    val genesisGroup: GenesisUrls? = null,
    val genesisOverridesFile: String,
    val publicUrl: String = "http://$keyName-api:8080",
    val pocCallbackUrl: String = publicUrl,
    val config: ApplicationConfig,
) {
    val composeFile = if (isGenesis) GENESIS_COMPOSE_FILE else NODE_COMPOSE_FILE
    val repoRoot = getRepoRoot()
    val composePath = Path.of(repoRoot, composeFile)

    fun dockerProcess(vararg args: String): ProcessBuilder {
        val envMap = this.getCommonEnvMap()
        return ProcessBuilder("docker", *args)
            .directory(composePath.parent.toFile())
            .also { it.environment().putAll(envMap) }
    }

    fun init() {
        tearDownExisting()
        setupFiles()
        val process = dockerProcess("compose", "-p", keyName, "-f", composeFile, "up", "-d")
            .start()
        process.waitFor()
    }

    fun tearDownExisting() {
        dockerProcess("compose", "-p", keyName, "down").start().waitFor()
    }

    private fun getCommonEnvMap(): Map<String, String> {
        return buildMap {
            put("KEY_NAME", keyName)
            put("NODE_HOST", "$keyName-node")
            put("DAPI_API__POC_CALLBACK_URL", pocCallbackUrl)
            put("DAPI_API__PUBLIC_URL", publicUrl)
            put("DAPI_CHAIN_NODE__IS_GENESIS", isGenesis.toString().lowercase())
            put("NODE_CONFIG_PATH", "/root/node_config.json")
            put("NODE_CONFIG", nodeConfigFile)
            put("PUBLIC_URL", publicUrl)
            put("PORT", port.toString())
            put("POC_CALLBACK_URL", publicUrl)
            put("IS_GENESIS", isGenesis.toString().lowercase())
            put("WIREMOCK_PORT", wiremockExternalPort.toString())
            put("GENESIS_OVERRIDES_FILE", genesisOverridesFile)
            genesisGroup?.let {
                put("SEED_NODE_RPC_URL", it.rpcUrl)
                put("SEED_NODE_P2P_URL", it.p2pUrl)
                put("SEED_API_URL", it.apiUrl)
            }
        }
    }

    @OptIn(ExperimentalPathApi::class)
    private fun setupFiles() {
        val baseDir = Path.of(workingDirectory)
        if (isGenesis) {
            val prodLocal = baseDir.resolve("prod-local")
            prodLocal.deleteRecursively()
        }

        val inferenceDir = baseDir.resolve("prod-local/$keyName")
        val mappingsDir = baseDir.resolve("prod-local/wiremock/$keyName/mappings")
        val filesDir = baseDir.resolve("prod-local/wiremock/$keyName/__files")
        val mappingsSourceDir = baseDir.resolve("testermint/src/main/resources/mappings")
        val publicHtmlDir = baseDir.resolve("public-html")

        Files.createDirectories(mappingsDir)
        Files.createDirectories(filesDir)
        Files.createDirectories(inferenceDir)
        mappingsSourceDir.copyToRecursively(mappingsDir, overwrite = true, followLinks = false)
        publicHtmlDir.copyToRecursively(filesDir, overwrite = true, followLinks = false)
        val jsonOverrides = config.genesisSpec?.toJson(cosmosJson)?.let { "{ \"app_state\": $it }" } ?: "{}"
        Files.writeString(inferenceDir.resolve("genesis_overrides.json"), jsonOverrides, StandardOpenOption.CREATE)
    }

    val apiUrl = "http://$keyName-api:8080"
    val rpcUrl = "http://$keyName-node:26657"
    val p2pUrl = "http://$keyName-node:26656"

    init {
        require(isGenesis || genesisGroup != null) { "Genesis group must be provided" }
    }
}

fun createDockerGroup(iteration: Int, genesisUrls: GenesisUrls?, config: ApplicationConfig): DockerGroup {
    val keyName = if (iteration == 0) "genesis" else "join$iteration"
    val nodeConfigFile = "node_payload_wiremock_$keyName.json"
    val repoRoot = getRepoRoot()

    val nodeFile = Path.of(repoRoot, nodeConfigFile)
    if (!Files.exists(nodeFile)) {
        Files.writeString(
            nodeFile, """
            [
              {
                "id": "wiremock",
                "host": "$keyName-wiremock",
                "inference_port": 8080,
                "poc_port": 8080,
                "max_concurrent": 10,
                "models": [
                  "unsloth/llama-3-8b-Instruct"
                ]
              }
            ]
        """.trimIndent()
        )
    }
    return DockerGroup(
        dockerClient = DockerClientBuilder.getInstance().build(),
        keyName = keyName,
        port = 8080 + iteration,
        nodeConfigFile = nodeConfigFile,
        isGenesis = iteration == 0,
        wiremockExternalPort = 8090 + iteration,
        workingDirectory = repoRoot,
        genesisOverridesFile = "inference-chain/test_genesis_overrides.json",
        genesisGroup = genesisUrls,
        config = config
    )
}

fun getRepoRoot(): String {
    val currentDir = Path.of("").toAbsolutePath()
    return generateSequence(currentDir) { it.parent }
        .firstOrNull { it.fileName.toString() == "inference-ignite" }
        ?.toString()
        ?: throw IllegalStateException("Repository root 'inference-ignite' not found")
}

fun initializeCluster(joinCount: Int = 0, config: ApplicationConfig): List<DockerGroup> {
    val genesisGroup = createDockerGroup(0, null, config)
    val joinGroups =
        (1..joinCount).map { createDockerGroup(it, GenesisUrls(genesisGroup.keyName.trimStart('/')), config) }
    val allGroups = listOf(genesisGroup) + joinGroups
    allGroups.forEach { it.tearDownExisting() }
    genesisGroup.init()
    Thread.sleep(40000)
    joinGroups.forEach { it.init() }
    return allGroups
}

fun initCluster(
    joinCount: Int = 2,
    config: ApplicationConfig = inferenceConfig,
    reboot: Boolean = false,
): LocalCluster {
    val cluster = setupLocalCluster(joinCount, config, reboot)
    initialize(cluster.allPairs)
    return cluster
}

fun setupLocalCluster(joinCount: Int, config: ApplicationConfig, reboot: Boolean = false): LocalCluster {
    val currentCluster = getLocalCluster(config)
    if (clusterMatchesConfig(currentCluster, joinCount, config) && !reboot) {
        return currentCluster
    } else {
        initializeCluster(joinCount, config)
        return getLocalCluster(config) ?: error("Local cluster not initialized")
    }
}

@OptIn(ExperimentalContracts::class)
fun clusterMatchesConfig(cluster: LocalCluster?, joinCount: Int, config: ApplicationConfig): Boolean {
    contract {
        returns(true) implies (cluster != null)
    }
    if (cluster == null) return false
    if (cluster.joinPairs.size != joinCount) return false
    val genesisState = cluster.genesis.node.getGenesisState()
    return config.genesisSpec?.matches(genesisState.appState) != false
}

fun getLocalCluster(config: ApplicationConfig): LocalCluster? {
    val currentPairs = getLocalInferencePairs(config)
    val (genesis, join) = currentPairs.partition { it.name == "/${config.genesisName}" }
    return genesis.singleOrNull()?.let {
        LocalCluster(it, join)
    }
}

data class LocalCluster(
    val genesis: LocalInferencePair,
    val joinPairs: List<LocalInferencePair>,
) {
    val allPairs = listOf(genesis) + joinPairs
    fun withAdditionalJoin(joinCount: Int = 1): LocalCluster {
        val newJoinGroups =
            (1..joinCount).map {
                createDockerGroup(
                    iteration = it + this.joinPairs.size,
                    genesisUrls = GenesisUrls(this.genesis.name.trimStart('/')),
                    config = this.genesis.config
                )
            }
        newJoinGroups.forEach { it.tearDownExisting() }
        newJoinGroups.forEach { it.init() }
        return getLocalCluster(this.genesis.config)!!
    }

    fun withConsumer(name: String, action: (Consumer) -> Unit) {
        val consumer = create(this, name)
        try {
            action(consumer)
        } finally {
            consumer.pair.node.close()
        }
    }

}

class Consumer(val name: String, val pair: LocalInferencePair, val address: String) {
    companion object {
        fun create(localCluster: LocalCluster, name: String): Consumer {
            val cli = ApplicationCLI(name, localCluster.genesis.config)
            cli.createContainer(doNotStartChain = true)
            val newKey = cli.createKey(name)
            localCluster.genesis.api.addUnfundedInferenceParticipant(
                UnfundedInferenceParticipant(
                    "",
                    listOf(),
                    "",
                    newKey.pubkey.key,
                    newKey.address
                )
            )
            // Need time to make sure consumer is added
            localCluster.genesis.node.waitForNextBlock(2)
            return Consumer(
                name = name,
                pair = LocalInferencePair(cli, localCluster.genesis.api, null, name, localCluster.genesis.config),
                address = newKey.address,
            )
        }
    }
}
