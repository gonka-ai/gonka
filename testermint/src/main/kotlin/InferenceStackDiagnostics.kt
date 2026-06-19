package com.productscience

import com.github.dockerjava.api.model.Container
import com.github.dockerjava.core.DockerClientBuilder
import org.tinylog.kotlin.Logger
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption

private val inferenceStackServices =
    listOf("node", "api", "postgres", "proxy", "mock-server", "versiond", "devshardd-artifact-server")

/** Written under testermint/logs/ and uploaded as part of the integration-test log artifact. */
const val INFERENCE_DOCKER_DUMP_DIR = "logs/docker-dump"

private const val DEFAULT_DOCKER_LOG_TAIL = 200

private const val VERSIOND_DOCKER_LOG_TAIL = 500

/**
 * Logs docker compose output and returns the process exit code.
 * Used for join/genesis stack bring-up so CI artifacts include compose failures.
 */
fun DockerGroup.runComposeLogged(context: String, vararg composeArgs: String): Int {
    val fullArgs = composeArgs.toList()
    Logger.info(
        "[{}] docker compose ({}) cmd: {}",
        pairName,
        context,
        fullArgs.joinToString(" "),
    )
    Logger.info(
        "[{}] compose context ({}): ACCOUNT_PUBKEY={}, coldAccountPubkey.len={}, KEYRING_BACKEND={}",
        pairName,
        context,
        if (coldAccountPubkey.isNullOrBlank()) "unset" else "set",
        coldAccountPubkey?.length ?: 0,
        if (isGenesis) "test" else "file",
    )
    val process = dockerProcess(*fullArgs.toTypedArray())
        .redirectErrorStream(true)
        .start()
    process.inputStream.bufferedReader().lines().forEach { line ->
        Logger.info("[{}] compose> {}", pairName, line)
    }
    val exitCode = process.waitFor()
    if (exitCode != 0) {
        Logger.error("[{}] docker compose ({}) exited with code {}", pairName, context, exitCode)
    }
    return exitCode
}

/** `docker compose ps -a` for this pair's project (running and exited services). */
fun DockerGroup.logComposeProjectState(context: String) {
  val args = listOf("ps", "-a")
  runComposeLogged("compose-ps-$context", *args.toTypedArray())
}

/** Lists inference-related containers for one pair, including stopped/exited. */
fun logInferenceStackContainers(pairName: String, context: String) {
    val dockerClient = DockerClientBuilder.getInstance().build()
    val all = dockerClient.listContainersCmd().withShowAll(true).exec()
    Logger.info("[{}] inference stack container scan ({})", pairName, context)
    inferenceStackServices.forEach { service ->
        val expected = listOf("$pairName-$service", "/$pairName-$service")
        val container = all.find { c -> c.names.any { it in expected } }
        if (container == null) {
            Logger.warn("[{}]   {}-{}: not found (never created or removed)", pairName, pairName, service)
        } else {
            Logger.info(
                "[{}]   {}: state={} status={} image={}",
                pairName,
                container.names.first(),
                container.state,
                container.status,
                container.image,
            )
            if (container.state != "running") {
                tailDockerLogs(container.names.first().trimStart('/'), lines = 100, context = context)
            }
        }
    }
}

/** Explains why [getLocalInferencePairs] cannot attach dapi log streams (pair discovery). */
fun logClusterPairMismatch(
    config: ApplicationConfig,
    nodes: List<Container>,
    apis: List<Container>,
    context: String,
) {
    Logger.error(
        "Cluster pair mismatch ({}): {} node container(s), {} api container(s) (image={})",
        context,
        nodes.size,
        apis.size,
        config.apiImageName,
    )
    nodes.forEach { n ->
        Logger.error("  node: name={} state={} status={}", n.names.first(), n.state, n.status)
    }
    apis.forEach { a ->
        Logger.error("  api:  name={} state={} status={}", a.names.first(), a.state, a.status)
    }
    val nodePairNames = nodes.mapNotNull { nameExtractor.find(it.names.first())?.groupValues?.get(1) }.toSet()
    val apiPairNames = apis.mapNotNull { container ->
        container.names.first().removePrefix("/").removeSuffix("-api").takeIf { it.isNotBlank() }
    }.toSet()
    val nodesWithoutApi = nodePairNames - apiPairNames
    val apisWithoutNode = apiPairNames - nodePairNames
    if (nodesWithoutApi.isNotEmpty()) {
        Logger.error("  pairs with node but no running api: {}", nodesWithoutApi)
        nodesWithoutApi.forEach { logInferenceStackContainers(it, context) }
    }
    if (apisWithoutNode.isNotEmpty()) {
        Logger.error("  pairs with api but no running node: {}", apisWithoutNode)
    }
    listOf("genesis", "join1", "join2").forEach { pair ->
        if (pair == "genesis" || pair.startsWith("join")) {
            logInferenceStackContainers(pair, "mismatch-$context")
        }
    }
}

/**
 * Captures `docker logs` for genesis/join inference stacks into [dumpDir].
 * Invoked on test failure so CI/local runs archive versiond output under testermint/logs/.
 */
fun dumpInferenceDockerLogsForArtifact(
    context: String,
    dumpDir: Path = Path.of(INFERENCE_DOCKER_DUMP_DIR),
    pairNames: List<String> = listOf("genesis", "join1", "join2"),
) {
    runCatching {
        Files.createDirectories(dumpDir)
        val inspectPath = dumpDir.resolve("inspect.txt")
        val missingPath = dumpDir.resolve("missing.txt")
        Files.writeString(inspectPath, "=== docker dump context: $context ===\n", StandardOpenOption.CREATE)
        Files.writeString(missingPath, "", StandardOpenOption.CREATE)

        val psProcess =
            ProcessBuilder("docker", "ps", "-a")
                .redirectErrorStream(true)
                .start()
        val psOutput = psProcess.inputStream.bufferedReader().use { it.readText() }
        psProcess.waitFor()
        Files.writeString(dumpDir.resolve("all-containers.txt"), psOutput, StandardOpenOption.CREATE)

        pairNames.forEach { pair ->
            inferenceStackServices.forEach serviceLoop@{ svc ->
                val container = "$pair-$svc"
                val tail = if (svc == "versiond") VERSIOND_DOCKER_LOG_TAIL else DEFAULT_DOCKER_LOG_TAIL
                if (!dockerContainerExists(container)) {
                    Files.writeString(
                        missingPath,
                        "missing container: $container\n",
                        StandardOpenOption.CREATE,
                        StandardOpenOption.APPEND,
                    )
                    return@serviceLoop
                }
                val state =
                    ProcessBuilder(
                        "docker",
                        "inspect",
                        "-f",
                        "state={{.State.Status}} exit={{.State.ExitCode}} err={{.State.Error}}",
                        container,
                    )
                        .redirectErrorStream(true)
                        .start()
                        .let { proc ->
                            proc.inputStream.bufferedReader().use { it.readText().trim() }.also { proc.waitFor() }
                        }
                Files.writeString(
                    inspectPath,
                    "=== $container $state ===\n",
                    StandardOpenOption.CREATE,
                    StandardOpenOption.APPEND,
                )
                captureDockerLogsToFile(container, tail, dumpDir.resolve("$container.log"))
            }
        }

        dumpComposeProjectStateForArtifact(dumpDir.resolve("compose-ps.txt"))

        Logger.info(
            "Inference docker logs captured under {} (context={}, versiond tail={})",
            dumpDir.toAbsolutePath(),
            context,
            VERSIOND_DOCKER_LOG_TAIL,
        )
    }.onFailure { e ->
        Logger.warn("Failed to capture inference docker logs for artifact: {}", e.message)
    }
}

private fun dockerContainerExists(containerName: String): Boolean {
    val proc =
        ProcessBuilder("docker", "inspect", containerName)
            .redirectErrorStream(true)
            .start()
    proc.waitFor()
    return proc.exitValue() == 0
}

private fun captureDockerLogsToFile(containerName: String, tail: Int, outputFile: Path) {
    val proc =
        ProcessBuilder("docker", "logs", "--tail", tail.toString(), containerName)
            .redirectErrorStream(true)
            .start()
    val output = proc.inputStream.bufferedReader().use { it.readText() }
    val exit = proc.waitFor()
    val header = "# docker logs --tail $tail $containerName (exit=$exit)\n"
    Files.writeString(
        outputFile,
        header + if (output.isBlank()) "(empty)\n" else output,
        StandardOpenOption.CREATE,
        StandardOpenOption.TRUNCATE_EXISTING,
    )
}

private fun dumpComposeProjectStateForArtifact(outputFile: Path) {
    val repoRoot = getRepoRoot()
    val localTestNet = Path.of(repoRoot, "local-test-net")
    if (!Files.isDirectory(localTestNet)) {
        return
    }
    Files.writeString(outputFile, "", StandardOpenOption.CREATE)
    fun appendComposePs(project: String, label: String, vararg composeFiles: String) {
        val args =
            buildList {
                add("docker")
                add("compose")
                add("-p")
                add(project)
                composeFiles.forEach { file -> add("-f"); add(file) }
                add("--project-directory")
                add(repoRoot)
                add("ps")
                add("-a")
            }
        Files.writeString(
            outputFile,
            "=== compose -p $project ($label) ===\n",
            StandardOpenOption.APPEND,
        )
        val proc =
            ProcessBuilder(*args.toTypedArray())
                .directory(localTestNet.toFile())
                .redirectErrorStream(true)
                .start()
        val out = proc.inputStream.bufferedReader().use { it.readText() }
        proc.waitFor()
        Files.writeString(outputFile, out + "\n", StandardOpenOption.APPEND)
    }
    runCatching {
        appendComposePs("genesis", "base+genesis", "docker-compose-base.yml", "docker-compose.genesis.yml")
        appendComposePs(
            "genesis",
            "base+genesis+versiond",
            "docker-compose-base.yml",
            "docker-compose.genesis.yml",
            "docker-compose.versiond.yml",
        )
        appendComposePs("join1", "base+join", "docker-compose-base.yml", "docker-compose.join.yml")
        appendComposePs("join2", "base+join", "docker-compose-base.yml", "docker-compose.join.yml")
    }.onFailure { e ->
        Files.writeString(
            outputFile,
            "compose ps failed: ${e.message}\n",
            StandardOpenOption.APPEND,
        )
    }
}

fun tailDockerLogs(containerName: String, lines: Int = 80, context: String = "") {
    val label = if (context.isBlank()) containerName else "$containerName ($context)"
    val process = ProcessBuilder("docker", "logs", "--tail", lines.toString(), containerName)
        .redirectErrorStream(true)
        .start()
    val output = process.inputStream.bufferedReader().readText()
    val exit = process.waitFor()
    if (exit != 0) {
        Logger.warn("Could not read docker logs for {} (exit={}): {}", label, exit, output.trim())
        return
    }
    if (output.isBlank()) {
        Logger.info("docker logs for {}: (empty)", label)
        return
    }
    Logger.info("docker logs for {} (last {} lines):", label, lines)
    output.lineSequence().take(200).forEach { line ->
        Logger.info("  | {}", line)
    }
}
