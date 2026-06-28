package com.productscience

import com.github.dockerjava.api.DockerClient
import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.data.AppState
import com.productscience.data.Spec
import com.productscience.data.UnfundedInferenceParticipant
import okhttp3.internal.toImmutableList
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
import kotlin.io.path.exists

const val GENESIS_KEY_NAME = "genesis"

private const val VERSIOND_COMPOSE_FILE = "docker-compose.versiond.yml"

/**
 * Docker platform for versiond + devshardd override binaries.
 * Mirrors [scripts/blst-portable.sh] so Apple Silicon runs linux/arm64 everywhere.
 */
internal fun resolveDockerPlatform(): String {
    System.getenv("DOCKER_PLATFORM")?.trim()?.takeIf { it.isNotEmpty() }?.let { return it }
    if (!System.getProperty("os.name").contains("Mac", ignoreCase = true)) {
        return "linux/amd64"
    }
    return runCatching {
        val proc =
            ProcessBuilder("sysctl", "-n", "hw.optional.arm64")
                .redirectErrorStream(true)
                .start()
        val arm64 = proc.inputStream.bufferedReader().readText().trim()
        proc.waitFor()
        if (proc.exitValue() == 0 && arm64 == "1") "linux/arm64" else "linux/amd64"
    }.getOrDefault("linux/amd64")
}

/** True when this pair's compose stack includes the versiond overlay (devshardd / VersiondTests). */
private fun DockerGroup.usesVersiondOverlay(): Boolean =
    composeFiles.any { it.endsWith(VERSIOND_COMPOSE_FILE) }

/**
 * Retry container lookup with exponential backoff.
 * Docker containers may not be immediately visible after `docker compose up`.
 */
fun retryGetCli(config: ApplicationConfig, pairName: String, maxAttempts: Int = 5): ApplicationCLI {
    var delay = 2000L // start at 2s
    for (attempt in 1..maxAttempts) {
        val containers = getRawContainers(config)
        val cli = containers.getCli(pairName)
        if (cli != null) return cli
        if (attempt < maxAttempts) {
            Logger.warn("Container not found for keyName={}, retrying in {}ms (attempt {}/{})", pairName, delay, attempt, maxAttempts)
            Thread.sleep(delay)
            delay = (delay * 2).coerceAtMost(15000L)
        }
    }
    error("Could not find node container for keyName=$pairName after $maxAttempts attempts")
}
const val LOCAL_TEST_NET_DIR = "local-test-net"
val DNS_COMPOSE_FILES = listOf(
    "$LOCAL_TEST_NET_DIR/docker-compose.dns.yml",
    "$LOCAL_TEST_NET_DIR/docker-compose.dns-overrides.yml",
)
val BASE_COMPOSE_FILES = listOf(
    "${LOCAL_TEST_NET_DIR}/docker-compose-base.yml",
)

// Genesis-only overlay that exposes its postgres host port for JDBC asserts.
// Join pairs keep their postgres reachable only from chain-public.
private val POSTGRES_HOST_OVERLAY = listOf("$LOCAL_TEST_NET_DIR/docker-compose.postgres.yml")

val GENESIS_COMPOSE_FILES = BASE_COMPOSE_FILES +
    "${LOCAL_TEST_NET_DIR}/docker-compose.genesis.yml" +
    DNS_COMPOSE_FILES +
    POSTGRES_HOST_OVERLAY
val NODE_COMPOSE_FILES = BASE_COMPOSE_FILES + "${LOCAL_TEST_NET_DIR}/docker-compose.join.yml" + DNS_COMPOSE_FILES

data class GenesisUrls(val keyName: String) {
    val apiUrl = "http://$keyName-api:9000"
    val rpcUrl = "http://$keyName-node:26657"
    val p2pUrl = "tcp://$keyName-node:26656"
}

data class DockerGroup(
    val dockerClient: DockerClient,
    val pairName: String,
    val publicPort: Int,
    val mlPort: Int,
    val adminPort: Int,
    val natsPort: Int,
    val nodeManagerGrpcPort: Int,
    val nodeConfigFile: String,
    val isGenesis: Boolean = false,
    val mockExternalPort: Int,
    val proxyPort: Int,
    val rpcPort: Int,
    val p2pPort: Int,
    val workingDirectory: String,
    val genesisGroup: GenesisUrls? = null,
    val genesisOverridesFile: String,
    // publicUrl is what dapi registers on chain as its participant.inference_url.
    // Mirrors production: chain points at the per-pair proxy, which routes
    // /v1/devshard/* to dapi (legacy in-process HostManager via the exempt
    // route mechanism) and /devshard/<version>/* to versiond when configured.
    val publicUrl: String = "http://$pairName-proxy",
    // pocCallbackUrl stays direct -- it's an internal mlnode -> dapi callback
    // on the ML server port, never routed through nginx.
    val pocCallbackUrl: String = "http://$pairName-api:9100",
    val config: ApplicationConfig,
    val useSnapshots: Boolean,
    val p2pExternalAddress: String = "$pairName-node:26656",
) {
    val warmKeyName = "$pairName-WARM"
    val coldKeyName = pairName
    val composeFiles = when (isGenesis) {
        true -> GENESIS_COMPOSE_FILES
        false -> NODE_COMPOSE_FILES
    }.let { baseFiles: List<String> ->
        val additionalFiles = config.additionalDockerFilesByKeyName[pairName] ?: emptyList()
        baseFiles + additionalFiles.map { "$LOCAL_TEST_NET_DIR/$it" }
    }.onEach { file: String ->
        if (!Path.of(workingDirectory, file).exists()) {
            error("A docker file doesn't exist: $file")
        }
    }

    fun dockerProcess(vararg args: String): ProcessBuilder {
        val envMap = this.getCommonEnvMap(useSnapshots)
        return ProcessBuilder("docker", *args)
            .directory(File(workingDirectory))
            .also { it.environment().putAll(envMap) }
    }

    val warmKeyPassword = this.pairName.padEnd(10, '0')

    // return the pubkey for the cold key
    fun createColdKey(): String {
        val command = listOf(
            "docker", "compose",
            "-p", pairName
        ) + composeFiles.flatMap { listOf("-f", it) } + listOf(
            "--project-directory", workingDirectory,
            "run", "--rm", "--no-deps", "api",
            "sh", "-c",
            """printf '%s\n%s\n' "${warmKeyPassword}" "${warmKeyPassword}" | inferenced keys add $coldKeyName --keyring-backend file"""
        )

        val process = ProcessBuilder(command)
            .directory(File(workingDirectory))
            .also { it.environment().putAll(getCommonEnvMap(useSnapshots)) }
            .start()

        val output = process.inputStream.bufferedReader().use { it.readText() }
        val errorOutput = process.errorStream.bufferedReader().use { it.readText() }

        process.waitFor()

        Logger.info("Cold key created: $output", "")
        if (errorOutput.isNotBlank()) Logger.warn("Errors during warm key creation: $errorOutput", "")

        val pubkeyRegex = """"key":"([^"]+)"""".toRegex()
        return pubkeyRegex.find(output)?.groupValues?.get(1)
            ?: throw IllegalStateException("Could not extract pubkey from output: $output")
    }

    fun createWarmKey(): String {
        val command = listOf(
            "docker", "compose",
            "-p", pairName
        ) + composeFiles.flatMap { listOf("-f", it) } + listOf(
            "--project-directory", workingDirectory,
            "run", "--rm", "--no-deps", "api",
            "sh", "-c",
            """printf '%s\n%s\n' "${warmKeyPassword}" "${warmKeyPassword}" | inferenced keys add $warmKeyName --keyring-backend file"""
        )

        val process = ProcessBuilder(command)
            .directory(File(workingDirectory))
            .also { it.environment().putAll(getCommonEnvMap(useSnapshots)) }
            .start()

        val output = process.inputStream.bufferedReader().use { it.readText() }
        val errorOutput = process.errorStream.bufferedReader().use { it.readText() }

        process.waitFor()

        Logger.info("Warm key created: $output", "")
        if (errorOutput.isNotBlank()) Logger.warn("Errors during warm key creation: $errorOutput", "")

        return output
    }

    fun init() {
        setupFiles()
        val accountPubKey = if (!isGenesis) {
            val accountPubkey = createColdKey()
            createWarmKey()
            accountPubkey
        } else ""
        val composeArgs = mutableListOf("compose", "-p", pairName)
        composeFiles.forEach { file ->
            composeArgs.addAll(listOf("-f", file))
        }
        composeArgs.addAll(listOf("--project-directory", workingDirectory))
        val baseArgs = composeArgs.toImmutableList()
        if (isGenesis && usesVersiondOverlay()) {
            // versiond stacks start api with chain-node in one `up -d`; api init-docker.sh
            // often exits before genesis creates the cold key. Boot chain-node first, then
            // the rest. Default genesis tests (no versiond overlay) keep the original path.
            Logger.info("Genesis + versiond overlay: starting chain-node before full stack", "")
            dockerProcess(*(baseArgs + listOf("up", "-d", "chain-node")).toTypedArray()).start().waitFor()
            waitForColdKeyInNodeContainer()
            coldAccountPubkey = extractColdPubkeyFromNodeContainer()
            Logger.info("Genesis cold ACCOUNT_PUBKEY extracted for api startup", "")
            dockerProcess(*(baseArgs + listOf("up", "-d")).toTypedArray()).start().waitFor()
        } else {
            composeArgs.addAll(listOf("up", "-d"))
            if (!isGenesis) {
                // This will allow us to get our consensus key and add the participant BEFORE we launch the API
                composeArgs.add("chain-node")
            }
            if (!isGenesis) {
                val exitCode = runComposeLogged("join-chain-node-up", *composeArgs.toTypedArray())
                if (exitCode != 0) {
                    logComposeProjectState("after-failed-chain-node-up")
                    logInferenceStackContainers(pairName, "after-failed-chain-node-up")
                }
            } else {
                val dockerProcess = dockerProcess(*composeArgs.toTypedArray())
                val process = dockerProcess.start()
                process.inputStream.bufferedReader().use { it.lines().forEach { line -> Logger.info(line, "") } }
                process.errorStream.bufferedReader().use { it.lines().forEach { line -> Logger.info(line, "") } }
                process.waitFor()
            }
        }
        if (!isGenesis) {
            Thread.sleep(Duration.ofSeconds(10))

            val node = retryGetCli(config, this.pairName)
            val validatorDeadline = System.nanoTime() + Duration.ofSeconds(90).toNanos()
            var validatorKey: String? = null
            while (validatorKey == null) {
                try {
                    validatorKey = node.getValidatorInfo().key
                } catch (e: Exception) {
                    Logger.warn("Validator key not yet available, waiting 5 seconds and trying again", "")
                    if (System.nanoTime() >= validatorDeadline) {
                        throw IllegalStateException("Failed to get validator info within 90 seconds", e)
                    }
                    Thread.sleep(Duration.ofSeconds(5))
                }
            }
            node.registerNewParticipant(
                publicUrl,
                accountPubKey,
                validatorKey,
                this.genesisGroup?.apiUrl ?: "http://genesis-api:9000"
            )
            node.waitForNextBlock(2)
            node.grantMlOpsPermissionsToWarmAccount()
            // Services to start after registration. Proxy is in base compose
            // and started by "up -d" without explicit naming. Versiond is
            // added when this pair's additional compose files include it.
            val joinServices = mutableListOf("api", "mock-server", "proxy")
            val additionalForPair = config.additionalDockerFilesByKeyName[pairName] ?: emptyList()
            if (additionalForPair.any { it.contains("versiond") }) {
                joinServices.add("versiond")
            }
            val startRemainingArgs = baseArgs + listOf("up", "-d") + joinServices
            this.coldAccountPubkey = node.getColdPubKey()
            Logger.info(
                "[{}] Starting join inference stack after registration: {}",
                pairName,
                joinServices.joinToString(),
            )
            val stackExit = runComposeLogged(
                "join-inference-stack-up",
                *startRemainingArgs.toTypedArray(),
            )
            logComposeProjectState("after-inference-stack-up")
            logInferenceStackContainers(pairName, "after-inference-stack-up")
            val apiContainer = "$pairName-api"
            if (stackExit != 0 || !dockerContainerRunning(apiContainer)) {
                Logger.error(
                    "[{}] {} not running after compose up (exit={}); dumping logs",
                    pairName,
                    apiContainer,
                    stackExit,
                )
                tailDockerLogs(apiContainer, lines = 150, context = "join-api-not-running")
                tailDockerLogs("$pairName-postgres", lines = 80, context = "join-postgres")
                tailDockerLogs("$pairName-proxy", lines = 40, context = "join-proxy")
            }
            Thread.sleep(Duration.ofSeconds(10))
            if (!dockerContainerRunning(apiContainer)) {
                logComposeProjectState("after-wait-api-still-down")
                logInferenceStackContainers(pairName, "after-wait-api-still-down")
                error(
                    "$apiContainer not running after join stack up (compose exit=$stackExit). " +
                        "See testermint/logs for compose + docker log output.",
                )
            }
        }
        if (isGenesis && usesVersiondOverlay()) {
            ensureGenesisApiRunning()
        }
        // Just register the log events. Skip while versiond genesis is still settling —
        // initializeCluster will discover pairs after RPC readiness.
        if (!(isGenesis && usesVersiondOverlay())) {
            if (!isGenesis) {
                logInferenceStackContainers(pairName, "before-getLocalInferencePairs")
            }
            getLocalInferencePairs(config)
        }
        print(
            "Genesis overrides file: $genesisOverridesFile | content: ${
                Files.readString(
                    Path.of(
                        workingDirectory,
                        genesisOverridesFile
                    )
                )
            }"
        )
    }

    private fun waitForColdKeyInNodeContainer(timeout: Duration = Duration.ofMinutes(3)) {
        val nodeContainer = "$pairName-node"
        val keyringBackend = if (isGenesis) "test" else "file"
        val deadline = System.nanoTime() + timeout.toNanos()
        while (System.nanoTime() < deadline) {
            val check = ProcessBuilder(
                "docker", "exec", nodeContainer,
                "inferenced", "keys", "show", coldKeyName,
                "--keyring-backend", keyringBackend,
                "--keyring-dir", "/root/.inference",
            ).redirectErrorStream(true).start()
            if (check.waitFor() == 0) {
                Logger.info("Cold key '{}' available in {}", coldKeyName, nodeContainer)
                return
            }
            Thread.sleep(Duration.ofSeconds(2))
        }
        error("Cold key '$coldKeyName' not found in $nodeContainer within ${timeout.seconds}s")
    }

    /** Reads the genesis cold pubkey from chain-node for ACCOUNT_PUBKEY (api init-docker.sh). */
    private fun extractColdPubkeyFromNodeContainer(): String {
        val nodeContainer = "$pairName-node"
        val keyringBackend = if (isGenesis) "test" else "file"
        val proc = ProcessBuilder(
            "docker", "exec", nodeContainer,
            "sh", "-c",
            "inferenced keys show \"$coldKeyName\" --pubkey --keyring-backend $keyringBackend " +
                "--keyring-dir /root/.inference | jq -r '.key'",
        ).redirectErrorStream(true).start()
        val pubkey = proc.inputStream.bufferedReader().use { it.readText().trim() }
        if (proc.waitFor() != 0 || pubkey.isEmpty()) {
            error("Failed to read cold pubkey from $nodeContainer (exit=${proc.exitValue()}, out=$pubkey)")
        }
        return pubkey
    }

    /** Restart genesis-api if init-docker.sh exited before the shared key existed. */
    internal fun ensureGenesisApiRunning() {
        val apiContainer = "$pairName-api"
        if (dockerContainerRunning(apiContainer)) {
            return
        }
        if (coldAccountPubkey == null) {
            coldAccountPubkey = extractColdPubkeyFromNodeContainer()
        }
        Logger.warn("API container {} not running; recreating with ACCOUNT_PUBKEY", apiContainer)
        val composeArgs = mutableListOf("compose", "-p", pairName)
        composeFiles.forEach { file ->
            composeArgs.addAll(listOf("-f", file))
        }
        composeArgs.addAll(listOf("--project-directory", workingDirectory, "up", "-d", "--force-recreate", "api"))
        dockerProcess(*composeArgs.toTypedArray()).start().waitFor()
        val deadline = System.nanoTime() + Duration.ofMinutes(2).toNanos()
        while (System.nanoTime() < deadline) {
            if (dockerContainerRunning(apiContainer)) {
                return
            }
            Thread.sleep(Duration.ofSeconds(2))
        }
        error("$apiContainer did not stay running (check: docker logs $apiContainer)")
    }

    private fun dockerContainerRunning(containerName: String): Boolean = isDockerContainerRunning(containerName)

    fun tearDownExisting() {
        Logger.info("Tearing down existing docker group with keyName={}", pairName)
        val composeArgs = mutableListOf("compose", "-p", pairName)
        composeFiles.forEach { file ->
            composeArgs.addAll(listOf("-f", file))
        }
        // -v removes the per-pair postgres-data volume so a rebooted cluster
        // starts on a clean database. Bind-mounted dapi state is cleaned up
        // separately by the launch scripts.
        composeArgs.addAll(listOf("--project-directory", workingDirectory, "down", "-v"))
        dockerProcess(*composeArgs.toTypedArray()).start().waitFor()
    }

    var coldAccountPubkey: String? = null

    private fun getCommonEnvMap(useSnapshots: Boolean): Map<String, String> {
        return buildMap {
            // Align versiond service platform with host-built devshardd (see docker-compose.versiond.yml).
            put("DOCKER_PLATFORM", resolveDockerPlatform())
            put("KEY_NAME", coldKeyName)
            put("VERSIOND_SIGNER_KEY_NAME", if (isGenesis) coldKeyName else warmKeyName)
            // Per-pair keyring backend. Genesis api creates its key inside the
            // container with `test` backend (init-docker.sh CREATE_KEY=true).
            // Joins create with `file` backend externally via createColdKey.
            // Setting it unconditionally lets sibling processes (devshardd)
            // load the key with the matching backend; existing dapi behavior
            // is preserved because the value matches what dapi already used.
            put("KEYRING_BACKEND", if (isGenesis) "test" else "file")
            coldAccountPubkey?.let {
                put("ACCOUNT_PUBKEY", it)
                put("KEYRING_PASSWORD", warmKeyPassword)
                put("CREATE_KEY", "false")
                // KEY_NAME in our docker/compose files is used as pair-name a LOT. We will need to unwind this
                // For now, docker-compose.join.yml adds "-WARM" to the env variable only.
//                put("KEY_NAME", warmKeyName)
            }
            put("KEYRING_PASSWORD", warmKeyPassword)
            put("NODE_HOST", "$pairName-node")
            put("DAPI_API__POC_CALLBACK_URL", pocCallbackUrl)
            put("DAPI_API__PUBLIC_URL", publicUrl)
            put("DAPI_API__PUBLIC_SERVER_PORT", "9000")
            put("DAPI_API__ML_SERVER_PORT", "9100")
            put("DAPI_API__ADMIN_SERVER_PORT", "9200")
            put("DAPI_CHAIN_NODE__IS_GENESIS", isGenesis.toString().lowercase())
            put("DAPI_TX_BATCHING__FLUSH_TIMEOUT_SECONDS", "1")
            put("DAPI_TX_BATCHING__VALIDATION_V2_FLUSH_SIZE", "1")
            put("DAPI_TX_BATCHING__VALIDATION_V2_FLUSH_TIMEOUT_SECONDS", "1")
            put("DAPI_TX_BATCHING__POC_COMMIT_INTERVAL_SECONDS", "1")
            put("DAPI_STATS_FILE_STORAGE_ENABLED", "true")
            put("NODE_CONFIG_PATH", "/root/node_config.json")
            put("NODE_CONFIG", nodeConfigFile)
            put("PUBLIC_URL", publicUrl)
            put("PUBLIC_SERVER_PORT", publicPort.toString())
            put("ML_SERVER_PORT", mlPort.toString())
            put("ADMIN_SERVER_PORT", adminPort.toString())
            put("NATS_SERVER_PORT", natsPort.toString())
            put("NODE_MANAGER_GRPC_PORT", nodeManagerGrpcPort.toString())
            put("POC_CALLBACK_URL", pocCallbackUrl)
            put("IS_GENESIS", isGenesis.toString().lowercase())
            put("WIREMOCK_PORT", mockExternalPort.toString())
            put("PROXY_PORT", proxyPort.toString())
            put("RPC_PORT", rpcPort.toString())
            put("P2P_PORT", p2pPort.toString())
            put("GENESIS_OVERRIDES_FILE", genesisOverridesFile)
            put("SYNC_WITH_SNAPSHOTS", useSnapshots.toString().lowercase())
            put("SNAPSHOT_INTERVAL", "100")
            put("SNAPSHOT_KEEP_RECENT", "5")
            put("REST_API_ACTIVE", "true")
            put("P2P_EXTERNAL_ADDRESS", p2pExternalAddress)

            genesisGroup?.let {
                if (useSnapshots) {
                    put("RPC_SERVER_URL_1", it.rpcUrl)
                    put("RPC_SERVER_URL_2", it.rpcUrl.replace("genesis", "join1"))
                }
                put("SEED_NODE_RPC_URL", it.rpcUrl)
                put("DAPI_CHAIN_NODE__URL", it.rpcUrl)
                put("SEED_NODE_P2P_URL", it.p2pUrl)
                put("SEED_API_URL", it.apiUrl)
            }

            // Each pair has its own postgres (see docker-compose-base.yml).
            put("PGHOST", "$pairName-postgres")
            put("PGPORT", "5432")
            put("PGDATABASE", "payloads")
            put("PGUSER", "payloads")
            put("PGPASSWORD", "test")

            // Test-supplied extras applied last so they override defaults.
            // DevshardStandaloneTests uses this to set VERSIOND_BINARY_NAME,
            // VERSIOND_FORCE, VERSIOND_OVERRIDE_<version>, VERSIOND_SERVICE_NAME (override tests).
            putAll(config.additionalEnvVars)
        }
    }

    @OptIn(ExperimentalPathApi::class)
    private fun setupFiles() {
        val baseDir = Path.of(workingDirectory)
        if (isGenesis) {
            val prodLocal = baseDir.resolve("prod-local")
            try {
                // Use Docker to clean up root-owned files on Linux
                val cleanupProcess = ProcessBuilder(
                    "docker", "run", "--rm",
                    "-v", "${baseDir.toAbsolutePath()}:/workdir",
                    "-w", "/workdir",
                    "alpine:3.19",
                    "rm", "-rf", "prod-local"
                )
                    .directory(baseDir.toFile())
                    .start()
                
                val exitCode = cleanupProcess.waitFor()
                if (exitCode != 0) {
                    val errorOutput = cleanupProcess.errorStream.bufferedReader().use { it.readText() }
                    Logger.warn("Docker cleanup failed with exit code {}: {}", exitCode, errorOutput)
                    // Fallback to regular deletion
                    prodLocal.deleteRecursively()
                } else {
                    Logger.info("Successfully cleaned prod-local directory using Docker", "")
                }
            } catch (e: Exception) {
                Logger.error("Error during cleanup: {}, attempting fallback", e.message)
                try {
                    prodLocal.deleteRecursively()
                } catch (fallbackException: FileSystemException) {
                    Logger.error("Fallback cleanup also failed: {}", fallbackException.message)
                }
            }
        }

        val inferenceDir = baseDir.resolve("prod-local/$pairName")
        val mappingsDir = baseDir.resolve("prod-local/mock-server/$pairName/mappings")
        val filesDir = baseDir.resolve("prod-local/mock-server/$pairName/__files")
        val mappingsSourceDir = baseDir.resolve("testermint/src/main/resources/mappings")
        val publicHtmlDir = baseDir.resolve("public-html")

        Files.createDirectories(mappingsDir)
        Files.createDirectories(filesDir)
        Files.createDirectories(inferenceDir)
        mappingsSourceDir.copyToRecursively(mappingsDir, overwrite = true, followLinks = false)

        val templatePath = "testermint/src/main/resources/alternative-mappings/validate_poc_batch.template.json"
        val templateContent = baseDir.resolve(templatePath).toFile().readText()
        val content = templateContent.replace("{{KEY_NAME}}", pairName)
        val mappingFile = mappingsDir.resolve("validate_poc_batch.json")
        Files.writeString(mappingFile, content)

        if (Files.exists(publicHtmlDir)) {
            publicHtmlDir.copyToRecursively(filesDir, overwrite = true, followLinks = false)
        }
        val jsonOverrides = config.genesisSpec?.toJson(cosmosJson)?.let { "{ \"app_state\": $it }" } ?: "{}"
        Files.writeString(inferenceDir.resolve("genesis_overrides.json"), jsonOverrides, StandardOpenOption.CREATE)
        Logger.info("Setup files for keyName={}", pairName)
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
    val keyName = if (iteration == 0) GENESIS_KEY_NAME else "join$joinIter"
    val nodeConfigFile = config.nodeConfigFileByKeyName[keyName]
        .let { fileOrNull: String? -> fileOrNull ?: "node_payload_mock_server_$keyName.json" }
        .let { file: String -> "$LOCAL_TEST_NET_DIR/$file" }
    val repoRoot = getRepoRoot()

    val nodeFile = Path.of(repoRoot, nodeConfigFile)
    if (!Files.exists(nodeFile)) {
        Files.writeString(
            nodeFile, """
            [
              {
                "id": "mock-server",
                "host": "$keyName-mock-server",
                "inference_port": 8080,
                "poc_port": 8080,
                "max_concurrent": 10,
                "models": [
                  "$defaultModel"
                ]
              }
            ]
        """.trimIndent()
        )
    }
    return DockerGroup(
        dockerClient = DockerClientBuilder.getInstance().build(),
        pairName = keyName,
        publicPort = 9000 + iteration,
        mlPort = 9001 + iteration,
        adminPort = 9002 + iteration,
        natsPort = 9004 + iteration,
        nodeManagerGrpcPort = 9400 + iteration,
        nodeConfigFile = nodeConfigFile,
        isGenesis = iteration == 0,
        mockExternalPort = 8090 + iteration,
        proxyPort = 8000 + iteration,
        rpcPort = 26657 + iteration,
        p2pPort = 26656 + iteration,
        workingDirectory = repoRoot,
        genesisOverridesFile = "inference-chain/test_genesis_overrides.json",
        genesisGroup = genesisUrls,
        config = config,
        useSnapshots = useSnapshots,
    )
}

private fun isDockerContainerRunning(containerName: String): Boolean {
    val proc = ProcessBuilder("docker", "inspect", "-f", "{{.State.Running}}", containerName)
        .redirectErrorStream(true)
        .start()
    val out = proc.inputStream.bufferedReader().use { it.readText().trim() }
    proc.waitFor()
    return proc.exitValue() == 0 && out == "true"
}

private fun pairRpcSynced(pair: LocalInferencePair, minHeight: Long = 1): Boolean =
    runCatching {
        val status = pair.node.getStatus()
        status.syncInfo.latestBlockHeight >= minHeight && !status.syncInfo.catchingUp
    }.getOrDefault(false)

private fun pairApiResponding(pair: LocalInferencePair): Boolean {
    val apiContainer = "${pair.name.trimStart('/')}-api"
    if (!isDockerContainerRunning(apiContainer)) {
        return false
    }
    return runCatching {
        pair.getParams()
        true
    }.getOrDefault(false)
}

private fun waitForClusterReadyBeforeInitialize(
    cluster: LocalCluster,
    timeout: Duration = Duration.ofSeconds(90),
) {
    Logger.info("Waiting for cluster readiness (RPC synced, APIs up)", "")
    val deadline = System.nanoTime() + timeout.toNanos()
    while (System.nanoTime() < deadline) {
        if (!pairRpcSynced(cluster.genesis) || !pairApiResponding(cluster.genesis)) {
            Thread.sleep(1000)
            continue
        }
        val genesisHeight = runCatching { cluster.genesis.getCurrentBlockHeight() }.getOrNull()
        val joinsReady = cluster.joinPairs.all { join ->
            pairRpcSynced(join) &&
                pairApiResponding(join) &&
                (genesisHeight == null || runCatching {
                    kotlin.math.abs(join.getCurrentBlockHeight() - genesisHeight) <= 2
                }.getOrDefault(false))
        }
        if (joinsReady) {
            Logger.info(
                "Cluster ready for initialize (genesis block {}, {} join(s))",
                genesisHeight,
                cluster.joinPairs.size,
            )
            return
        }
        Thread.sleep(1000)
    }
    error("Cluster not ready for initialize within ${timeout.seconds} seconds")
}

fun getRepoRoot(): String {
    // Allow an explicit override so worktrees / additional checkouts (e.g.
    // gonka-2) can run tests without renaming their directory.
    System.getenv("GONKA_REPO_ROOT")?.takeIf { it.isNotBlank() }?.let { return it }

    val currentDir = Path.of("").toAbsolutePath().normalize()
    return generateSequence(currentDir) { it.parent }
        .firstOrNull { candidate ->
            Files.isDirectory(candidate.resolve("testermint")) &&
                Files.isDirectory(candidate.resolve("local-test-net")) &&
                Files.isDirectory(candidate.resolve("versioned"))
        }
        ?.toString()
        ?: throw IllegalStateException("Repository root not found from $currentDir (set GONKA_REPO_ROOT to override)")
}

fun initializeCluster(joinCount: Int = 0, config: ApplicationConfig, currentCluster: LocalCluster?): List<DockerGroup> {
    TestState.rebooting = true
    try {
        val genesisGroup = createDockerGroup(0, 0, null, config, false)
        val joinSize = currentCluster?.joinPairs?.size ?: 0
        if (joinSize > joinCount) {
            (joinCount until joinSize).mapIndexed { _, index ->
                val actualIndex = (index + 1) * 10
                createDockerGroup(
                    index + 1,
                    actualIndex,
                    GenesisUrls(genesisGroup.pairName.trimStart('/')),
                    config,
                    false
                )
            }.forEach { it.tearDownExisting() }
        }
        val joinGroups = (1..joinCount).mapIndexed { index, _ ->
            val actualIndex = (index + 1) * 10
            createDockerGroup(index + 1, actualIndex, GenesisUrls(genesisGroup.pairName.trimStart('/')), config, false)
        }
        val allGroups = listOf(genesisGroup) + joinGroups
        Logger.info("Initializing cluster with {} nodes", allGroups.size)
        allGroups.forEach { it.tearDownExisting() }
        genesisGroup.init()
        Thread.sleep(Duration.ofSeconds(30L))
        val genesisNode = retryGetCli(config, genesisGroup.pairName)
        Logger.info("Waiting for genesis RPC readiness", "")
        val readinessDeadline = System.nanoTime() + Duration.ofSeconds(90).toNanos()
        while (true) {
            try {
                if (genesisNode.getStatus().syncInfo.latestBlockHeight >= 1) {
                    break
                }
            } catch (e: Exception) {
                Logger.debug("Genesis RPC not ready yet: ${e.message}", "")
            }
            if (System.nanoTime() >= readinessDeadline) {
                error("Genesis RPC did not become ready within 90 seconds")
            }
            Thread.sleep(1000)
        }
        if (genesisGroup.usesVersiondOverlay()) {
            genesisGroup.ensureGenesisApiRunning()
        }
        val genesisPair = getLocalInferencePairs(config)
            .firstOrNull { it.name == genesisGroup.pairName || it.name == "/${genesisGroup.pairName}" }
            ?: error("Could not find local inference pair for keyName=${genesisGroup.pairName}")
        Logger.info("Waiting for genesis API and ML nodes readiness", "")
        genesisPair.waitForMlNodesToLoad(maxWaitAttempts = 18)
        if (joinGroups.isNotEmpty()) {
            val failures = java.util.Collections.synchronizedList(mutableListOf<Throwable>())
            joinGroups.map { group ->
                Thread {
                    try {
                        group.init()
                    } catch (e: Throwable) {
                        failures.add(e)
                    }
                }.apply {
                    name = "join-init-${group.pairName}"
                    start()
                }
            }.forEach { it.join() }
            failures.firstOrNull()?.let { throw it }
        }
        return allGroups
    } finally {
        TestState.rebooting = false
    }
}

fun initCluster(
    joinCount: Int = 2,
    config: ApplicationConfig = inferenceConfig,
    reboot: Boolean = false,
    resetMlNodes: Boolean = true,
    mergeSpec: Spec<AppState>? = null,
): Pair<LocalCluster, LocalInferencePair> {
    logSection("Cluster Discovery")
    val finalConfig = mergeSpec?.let {
        config.copy(genesisSpec = config.genesisSpec?.merge(mergeSpec))
    } ?: config
    val rebootFlagOn = Files.deleteIfExists(Path.of("reboot.txt"))
    val cluster = try {
        val c = setupLocalCluster(joinCount, finalConfig, reboot || rebootFlagOn)
        waitForClusterReadyBeforeInitialize(c)
        logSection("Found cluster, initializing")
        initialize(c.allPairs, resetMlNodes = resetMlNodes)
        c
    } catch (e: Exception) {
        Logger.error(e, "Failed to initialize cluster")
        if (reboot) {
            Logger.error(e, "Failed to initialize cluster, rebooting")
            throw e
        }
        Logger.error(e, "Error initializing cluser, retrying")
        logSection("Exception during cluster initialization, retrying")
        return initCluster(joinCount, finalConfig, reboot = true)
    }
    logSection("Cluster Initialized")
    cluster.allPairs.forEach {
        Logger.info("${it.name} has account ${it.node.getColdAddress()}", "")
    }
    return cluster to cluster.genesis
}

fun setupLocalCluster(joinCount: Int, config: ApplicationConfig, reboot: Boolean = false): LocalCluster {
    val currentCluster = try {
        getLocalCluster(config)
    } catch (e: InvalidClusterException) {
        Logger.error(e, "Cluster is in invalid state, rebooting")
        logSection("Invalid cluster, retrying")
        null
    }
    if (!reboot && clusterMatchesConfig(currentCluster, joinCount, config)) {
        return currentCluster
    } else {
        if (!reboot) {
            logSection("Cluster does not match config, rebooting")
        }
        if (reboot) {
            logSection("Rebooting cluster by request")
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
        LocalCluster(it, join.sortedBy { it.name })
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
                    useSnapshots = true,
                )
            }
        newJoinGroups.forEach { it.tearDownExisting() }
        newJoinGroups.forEach { it.init() }
        return getLocalCluster(this.genesis.config)!!
    }

    fun withConsumer(name: String, action: (Consumer) -> Unit) {
        val consumer = Consumer.create(this, name)
        try {
            action(consumer)
        } finally {
            consumer.pair.node.close()
        }
    }

    fun waitForMlNodesToLoad() {
        Logger.info("Waiting for ML nodes to load", "")
        allPairs.forEach { pair -> pair.waitForMlNodesToLoad() }
    }
}

class Consumer(val name: String, val pair: LocalInferencePair, val address: String) {
    companion object {
        fun create(localCluster: LocalCluster, name: String): Consumer {
            val newConfig = localCluster.genesis.config.copy(execName = localCluster.genesis.config.appName)
            val dockerExec = DockerExecutor(
                name,
                newConfig,
            )
            val cli = ApplicationCLI(
                newConfig,
                LogOutput(name, "consumer"),
                dockerExec,
                listOf(),
            )
            cli.createContainer(doNotStartChain = true)
            val newKey = cli.createKey(name)
            localCluster.genesis.api.addUnfundedInferenceParticipant(
                UnfundedInferenceParticipant(
                    "",
                    listOf(),
                    "",
                    newKey.pubkey.key,
                    newKey.address,
                ),
            )
            localCluster.genesis.node.waitForNextBlock(2)
            return Consumer(
                name = name,
                pair = LocalInferencePair(cli, localCluster.genesis.api, null, name, localCluster.genesis.config),
                address = newKey.address,
            )
        }
    }
}
