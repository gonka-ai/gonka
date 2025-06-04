package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.Consumer.Companion.create
import com.productscience.data.UnfundedInferenceParticipant
import org.tinylog.Logger
import java.io.File
import java.nio.file.FileSystemException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption
import java.time.Duration
import kotlin.contracts.ExperimentalContracts
import kotlin.contracts.contract
import kotlin.io.path.ExperimentalPathApi
import kotlin.io.path.copyToRecursively
import kotlin.io.path.deleteRecursively

const val LOCAL_TEST_NET_DIR = "local-test-net"
const val GENESIS_COMPOSE_FILE = "${LOCAL_TEST_NET_DIR}/docker-compose-local-genesis.yml"
const val NODE_COMPOSE_FILE = "${LOCAL_TEST_NET_DIR}/docker-compose-local.yml"

data class GenesisUrls(val keyName: String) {
    val apiUrl = "http://$keyName-api:9000"
    val rpcUrl = "http://$keyName-node:26657"
    val p2pUrl = "http://$keyName-node:26656"
}

data class DockerGroup(
    val dockerClient: DockerClient,
    val keyName: String,
    val publicPort: Int,
    val mlPort: Int,
    val adminPort: Int,
    val nodeConfigFile: String,
    val isGenesis: Boolean = false,
    val wiremockExternalPort: Int,
    val workingDirectory: String,
    val genesisGroup: GenesisUrls? = null,
    val genesisOverridesFile: String,
    val publicUrl: String = "http://$keyName-api:9000",
    val pocCallbackUrl: String = "http://$keyName-api:9100",
    val config: ApplicationConfig,
    val useSnapshots: Boolean,
) {
    val composeFile = if (isGenesis) GENESIS_COMPOSE_FILE else NODE_COMPOSE_FILE

    fun dockerProcess(vararg args: String): ProcessBuilder {
        val envMap = this.getCommonEnvMap(useSnapshots)
        return ProcessBuilder("docker", *args)
            .directory(File(workingDirectory))
            .also { it.environment().putAll(envMap) }
    }

    fun init() {
        tearDownExisting()
        setupFiles()
        val dockerProcess = dockerProcess(
            "compose",
            "-p",
            keyName,
            "-f",
            composeFile,
            "--project-directory",
            workingDirectory,
            "up",
            "-d"
        )
        val process = dockerProcess.start()
        process.inputStream.bufferedReader().lines().forEach { Logger.info(it, "") }
        process.errorStream.bufferedReader().lines().forEach { Logger.info(it, "") }
        process.waitFor()
        // Just register the log events
        getLocalInferencePairs(config)
    }

    fun tearDownExisting() {
        Logger.info("Tearing down existing docker group with keyName={}", keyName)
        dockerProcess("compose", "-p", keyName, "--project-directory", workingDirectory, "down").start().waitFor()
    }

    private fun getCommonEnvMap(useSnapshots: Boolean): Map<String, String> {
        return buildMap {
            put("KEY_NAME", keyName)
            put("NODE_HOST", "$keyName-node")
            put("DAPI_API__POC_CALLBACK_URL", pocCallbackUrl)
            put("DAPI_API__PUBLIC_URL", publicUrl)
            put("DAPI_API__PUBLIC_SERVER_PORT", "9000")
            put("DAPI_API__ML_SERVER_PORT", "9100")
            put("DAPI_API__ADMIN_SERVER_PORT", "9200")
            put("DAPI_CHAIN_NODE__IS_GENESIS", isGenesis.toString().lowercase())
            put("NODE_CONFIG_PATH", "/root/node_config.json")
            put("NODE_CONFIG", nodeConfigFile)
            put("PUBLIC_URL", publicUrl)
            put("PUBLIC_SERVER_PORT", publicPort.toString())
            put("ML_SERVER_PORT", mlPort.toString())
            put("ADMIN_SERVER_PORT", adminPort.toString())
            put("POC_CALLBACK_URL", pocCallbackUrl)
            put("IS_GENESIS", isGenesis.toString().lowercase())
            put("WIREMOCK_PORT", wiremockExternalPort.toString())
            put("GENESIS_OVERRIDES_FILE", genesisOverridesFile)
            put("SYNC_WITH_SNAPSHOTS", useSnapshots.toString().lowercase())
            put("SNAPSHOT_INTERVAL", "100")
            put("SNAPSHOT_KEEP_RECENT", "5")

            genesisGroup?.let {
                if (useSnapshots) {
                    put("RPC_SERVER_URL_1", it.rpcUrl)
                    put("RPC_SERVER_URL_2", it.rpcUrl.replace("genesis", "join1"))
                }
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
            try {
                prodLocal.deleteRecursively()
            } catch (e: FileSystemException) {
                val rootCauses = mutableSetOf<Throwable>()
                fun extractRootCause(throwable: Throwable) {
                    throwable.cause?.let { cause ->
                        if (!rootCauses.contains(cause)) {
                            rootCauses.add(cause)
                            extractRootCause(cause)
                        }
                    }
                    throwable.suppressed.forEach { suppressed ->
                        if (!rootCauses.contains(suppressed)) {
                            rootCauses.add(suppressed)
                            extractRootCause(suppressed)
                        }
                    }
                }
                extractRootCause(e)
                rootCauses.forEach { cause ->
                    Logger.error("Root cause error deleting directory: {} ({})", cause.message, cause.javaClass.name)
                }
            }
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

        if (Files.exists(publicHtmlDir)) {
            publicHtmlDir.copyToRecursively(filesDir, overwrite = true, followLinks = false)
        }
        val jsonOverrides = config.genesisSpec?.toJson(cosmosJson)?.let { "{ \"app_state\": $it }" } ?: "{}"
        Files.writeString(inferenceDir.resolve("genesis_overrides.json"), jsonOverrides, StandardOpenOption.CREATE)
        Logger.info("Setup files for keyName={}", keyName)
    }
    init {
        require(isGenesis || genesisGroup != null) { "Genesis group must be provided" }
    }
}

fun createDockerGroup(
    joinIter: Int,
    iteration: Int,
    genesisUrls: GenesisUrls?,
    config: ApplicationConfig,
    useSnapshots: Boolean
): DockerGroup {
    val keyName = if (iteration == 0) "genesis" else "join$joinIter"
    val nodeConfigFile = "${LOCAL_TEST_NET_DIR}/node_payload_wiremock_$keyName.json"
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
        publicPort = 9000 + iteration,
        mlPort = 9001 + iteration,
        adminPort = 9002 + iteration,
        nodeConfigFile = nodeConfigFile,
        isGenesis = iteration == 0,
        wiremockExternalPort = 8090 + iteration,
        workingDirectory = repoRoot,
        genesisOverridesFile = "inference-chain/test_genesis_overrides.json",
        genesisGroup = genesisUrls,
        config = config,
        useSnapshots = useSnapshots,
    )
}

fun getRepoRoot(): String {
    val currentDir = Path.of("").toAbsolutePath()
    return generateSequence(currentDir) { it.parent }
        .firstOrNull { it.fileName.toString() == "inference-ignite" }
        ?.toString()
        ?: throw IllegalStateException("Repository root 'inference-ignite' not found")
}

fun initializeCluster(joinCount: Int = 0, config: ApplicationConfig, currentCluster: LocalCluster?): List<DockerGroup> {
    val genesisGroup = createDockerGroup(0, 0, null, config, false)
    val joinSize = currentCluster?.joinPairs?.size ?: 0
    if (joinSize > joinCount) {
        (joinCount until joinSize).mapIndexed { _, index ->
            val actualIndex = (index + 1) * 10
            createDockerGroup(index + 1, actualIndex, GenesisUrls(genesisGroup.keyName.trimStart('/')), config, false)
        }.forEach { it.tearDownExisting() }
    }
    val joinGroups = (1..joinCount).mapIndexed { index, _ ->
        val actualIndex = (index + 1) * 10
        createDockerGroup(index + 1, actualIndex, GenesisUrls(genesisGroup.keyName.trimStart('/')), config, false)
    }
    val allGroups = listOf(genesisGroup) + joinGroups
    Logger.info("Initializing cluster with {} nodes", allGroups.size)
    allGroups.forEach { it.tearDownExisting() }
    genesisGroup.init()
    Thread.sleep(Duration.ofSeconds(20L))
    joinGroups.forEach { it.init() }
    return allGroups
}

fun initCluster(
    joinCount: Int = 2,
    config: ApplicationConfig = inferenceConfig,
    reboot: Boolean = false,
): Pair<LocalCluster, LocalInferencePair> {
    logSection("Cluster Discovery")
    val rebootFlagOn = Files.deleteIfExists(Path.of("reboot.txt"))
    val cluster = setupLocalCluster(joinCount, config, reboot || rebootFlagOn)
    Thread.sleep(50000)
    try {
        logSection("Found cluster, initializing")
        initialize(cluster.allPairs)
    } catch (e: Exception) {
        Logger.error(e, "Failed to initialize cluster")
        if (reboot) {
            Logger.error(e, "Failed to initialize cluster, rebooting")
            throw e
        }
        Logger.error(e, "Error initializing cluser, retrying")
        logSection("Exception during cluster initialization, retrying")
        return initCluster(joinCount, config, reboot = true)
    }
    logSection("Cluster Initialized")
    cluster.allPairs.forEach {
        Logger.info("${it.name} has account ${it.node.getAddress()}", "")
    }
    return cluster to cluster.genesis
}

fun setupLocalCluster(joinCount: Int, config: ApplicationConfig, reboot: Boolean = false): LocalCluster {
    val currentCluster = getLocalCluster(config)
    val size = currentCluster?.joinPairs?.size ?: 0
    if (!reboot && clusterMatchesConfig(currentCluster, joinCount, config)) {
        return currentCluster
    } else {
        if (!reboot) {
            logSection("Cluster does not match config, rebooting")
        }
        initializeCluster(joinCount, config, currentCluster)
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
    if (genesis.size != 1) {
        Logger.error("Expected exactly one genesis pair, found ${genesis.size}", "")
    }
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
        val currentMaxJoin = this.joinPairs.size
        val newMaxJoin = currentMaxJoin + joinCount
        val newJoinGroups =
            (currentMaxJoin + 1..newMaxJoin).map {
                createDockerGroup(
                    it,
                    iteration = it * 10,
                    genesisUrls = GenesisUrls(this.genesis.name.trimStart('/')),
                    config = this.genesis.config,
                    useSnapshots = true
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
            // TODO: Add Kube creation
            val newConfig = localCluster.genesis.config.copy(execName = localCluster.genesis.config.appName)
            val dockerExec = DockerExecutor(
                name,
                newConfig,
            )
            val cli = ApplicationCLI(
                newConfig,
                LogOutput(name, "consumer"),
                dockerExec,
                listOf()
            )
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
