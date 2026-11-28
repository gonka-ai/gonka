package com.productscience

import com.google.gson.Gson
import com.google.gson.annotations.SerializedName
import org.tinylog.kotlin.Logger
import java.io.BufferedReader
import java.io.InputStreamReader
import java.nio.file.Files
import java.nio.file.Path
import java.util.concurrent.ConcurrentHashMap
import kotlin.concurrent.thread

const val gatewayPort = 18080
const val gatewayLogPath = "/tmp/gateway.log"
const val gatewayBaseUrl = "http://localhost:$gatewayPort"
internal const val gatewaySettleAttempts = 30
internal const val gatewaySettleTimeoutSeconds = 300
internal const val gatewaySettleRetryMillis = 2_000L
internal const val httpStatusConflict = 409

private const val gatewayBinaryName = "gateway"
private const val gatewayContainerBinaryPath = "/usr/local/bin/$gatewayBinaryName"
private const val gatewayStorageDir = "/tmp/gateway"
private const val gatewayEscrowKeyVariablePrefix = "GATEWAY_ESCROW_KEY_"
private const val gatewayPollIntervalMillis = 500L
private const val gatewayHealthAttempts = 120
private const val gatewayStopAttempts = 60
private const val gatewayActivateAttempts = 60
private const val gatewayRoutableAttempts = 120
private const val httpStatusOk = 200
private const val httpStatusCodeLength = 3
private const val gatewayAdminTimeoutSeconds = 30
private const val gatewayActivateTimeoutSeconds = 120
private const val gatewayConnectTimeoutSeconds = 5
private const val gatewayAnswerPreviewLength = 200
private const val gatewayLogTailLines = 200
private const val chainGrpcPort = 9090
private const val chainRpcPort = 26657
private const val publicApiPort = 9000
private const val nodeManagerPort = 9400
private const val testPocRequestsPerWeight = 1_000_000
private const val probeAnswered = "OK"
internal val httpSuccessStatuses = 200..299
internal val noGatewayAnswer = GatewayHttpResult(0, "")

data class GatewayEscrowSeed(
    val escrowId: Long,
    val model: String,
    val routePrefix: String,
    val privateKey: String,
) {
    val privateKeyVariable: String
        get() = "$gatewayEscrowKeyVariablePrefix$escrowId"
}

private data class GatewaySeedJson(
    val id: String,
    val model: String,
    @SerializedName("private_key_env")
    val privateKeyEnv: String,
    @SerializedName("route_prefix")
    val routePrefix: String,
)

private class GatewayContainerState {
    val seeds = LinkedHashMap<Long, GatewayEscrowSeed>()
    var runningSeeds: Map<Long, GatewayEscrowSeed> = emptyMap()
    var logFollower: GatewayLogFollower? = null
    var storageClaimed = false
}

private val gatewayContainerStates = ConcurrentHashMap<String, GatewayContainerState>()

private val containersWithGateway = ConcurrentHashMap.newKeySet<String>()

/** Tails the gateway's stdout/stderr from the api container into tinylog with source=gateway. */
private class GatewayLogFollower(
    private val containerId: String,
    private val logFile: String,
    private val pairName: String,
) {
    @Volatile
    private var process: Process? = null

    private val tailCommand = "tail -n 0 -F $logFile"

    private val followerThread = thread(name = "gateway-log-$containerId", isDaemon = true) {
        logContext(
            mapOf(
                "pair" to pairName,
                "source" to "gateway",
                "operation" to "base",
            ),
        ) {
            try {
                val tail = ProcessBuilder("docker", "exec", containerId, "sh", "-c", "exec $tailCommand")
                    .redirectErrorStream(true)
                    .start()
                process = tail
                BufferedReader(InputStreamReader(tail.inputStream)).use { reader ->
                    var line: String?
                    while (reader.readLine().also { line = it } != null) {
                        if (Thread.currentThread().isInterrupted) {
                            break
                        }
                        val text = line!!.trim()
                        if (text.isNotEmpty()) {
                            Logger.info(text)
                        }
                    }
                }
            } catch (_: InterruptedException) {
            } finally {
                process?.destroyForcibly()
                process = null
            }
        }
    }

    /** Killing the local `docker exec` client leaves its `tail` running in the container, so the tail is killed there too. */
    fun stop() {
        followerThread.interrupt()
        process?.destroyForcibly()
        runCatching {
            ProcessBuilder("docker", "exec", containerId, "pkill", "-f", tailCommand)
                .redirectErrorStream(true)
                .start()
                .waitFor()
        }
    }
}

internal fun LocalInferencePair.apiContainerId(): String {
    val executor = api.executor
    require(executor is DockerExecutor) { "the gateway runs only in a DockerExecutor-backed api container" }
    return executor.containerId
}

/** Copies the host-built Linux `build/gateway` into the api container once; the api image does not ship it. */
internal fun LocalInferencePair.ensureGatewayInstalled() {
    val containerId = apiContainerId()
    if (containersWithGateway.contains(containerId)) {
        return
    }
    synchronized(containersWithGateway) {
        if (containersWithGateway.contains(containerId)) {
            return
        }
        val hostBinary = Path.of(getRepoRoot(), "build", gatewayBinaryName)
        check(Files.isRegularFile(hostBinary)) {
            "gateway binary missing at $hostBinary. Run: make gateway-build (produces a Linux binary for docker exec)"
        }
        val copy = ProcessBuilder(
            "docker",
            "cp",
            hostBinary.toAbsolutePath().toString(),
            "$containerId:$gatewayContainerBinaryPath",
        )
            .redirectErrorStream(true)
            .start()
        val copyOutput = copy.inputStream.bufferedReader().use { it.readText() }
        check(copy.waitFor() == 0) {
            "docker cp build/gateway into $containerId failed: $copyOutput"
        }
        api.executor.exec(listOf("chmod", "+x", gatewayContainerBinaryPath), null)
        containersWithGateway.add(containerId)
        Logger.info("Installed host build/gateway into api container {}", containerId)
    }
}

/** The gateway reads signing keys from its own environment, so a key it was not started with restarts it with every seed; its store survives. */
internal fun LocalInferencePair.serveEscrowThroughGateway(seed: GatewayEscrowSeed) {
    ensureGatewayInstalled()
    val containerId = apiContainerId()
    val state = gatewayContainerStates.computeIfAbsent(containerId) { GatewayContainerState() }
    synchronized(state) {
        state.seeds[seed.escrowId] = seed
        val servesSeed = state.runningSeeds[seed.escrowId] == seed
        if (!servesSeed || !isGatewayHealthy()) {
            restartGateway(containerId, state)
        }
    }
    activateEscrow(seed.escrowId)
    waitForEscrowRoutable(seed.escrowId)
}

internal fun LocalInferencePair.deactivateEscrowInGateway(escrowId: Long) {
    runCatching {
        gatewayAdminRequest("POST", "/v1/admin/devshards/$escrowId/deactivate")
    }.onFailure {
        Logger.info("Deactivating escrow {} in the gateway failed: {}", escrowId, it.message)
    }
}

internal fun LocalInferencePair.forgetGatewayEscrow(escrowId: Long) {
    gatewayContainerStates[apiContainerId()]?.let { state ->
        synchronized(state) { state.seeds.remove(escrowId) }
    }
}

data class GatewayHttpResult(val httpCode: Int, val body: String)

internal fun LocalInferencePair.gatewayAdminRequest(
    method: String,
    path: String,
    maxTimeSeconds: Int = gatewayAdminTimeoutSeconds,
): GatewayHttpResult {
    val bodyFile = "/tmp/gateway-admin-${System.nanoTime()}.out"
    val raw = api.executor.exec(
        listOf(
            "sh", "-c",
            "curl --silent --show-error --connect-timeout $gatewayConnectTimeoutSeconds --max-time $maxTimeSeconds " +
                "-o $bodyFile -w '%{http_code}' -X $method '$gatewayBaseUrl$path' " +
                "-H 'Authorization: Bearer $devshardAdminApiKey'",
        ),
        null,
    ).joinToString("").trim()
    val httpCode = raw.takeLast(httpStatusCodeLength).toIntOrNull()
        ?: error("gateway $method $path did not return an HTTP status code (got ${raw.take(gatewayAnswerPreviewLength)})")
    val body = runCatching {
        api.executor.exec(listOf("sh", "-c", "cat $bodyFile; rm -f $bodyFile"), null).joinToString("")
    }.getOrDefault("")
    return GatewayHttpResult(httpCode, body)
}

private fun LocalInferencePair.isGatewayHealthy(): Boolean = gatewayAnswers("/healthz")

private fun LocalInferencePair.gatewayAnswers(path: String): Boolean = runCatching {
    api.executor.exec(
        listOf("sh", "-c", "curl -sf $gatewayBaseUrl$path >/dev/null 2>&1 && echo $probeAnswered"),
        null,
    ).any { it.trim() == probeAnswered }
}.getOrDefault(false)

private fun LocalInferencePair.restartGateway(containerId: String, state: GatewayContainerState) {
    state.logFollower?.stop()
    state.logFollower = null
    stopGatewayProcess()
    // The store of a gateway an earlier JVM started holds escrows whose keys this JVM never passes.
    if (!state.storageClaimed) {
        api.executor.exec(listOf("rm", "-rf", gatewayStorageDir), null)
        state.storageClaimed = true
    }
    val seeds = state.seeds.values.toList()
    val seedsJson = Gson().toJson(
        seeds.map { seed ->
            GatewaySeedJson(seed.escrowId.toString(), seed.model, seed.privateKeyVariable, seed.routePrefix)
        },
    )
    val keyAssignments = seeds.joinToString("") { seed -> " ${seed.privateKeyVariable}='${seed.privateKey}'" }
    val startCommand = listOf(
        "sh", "-c",
        "DEVSHARDS_JSON='$seedsJson'" +
            keyAssignments +
            " DEVSHARD_ADMIN_API_KEY='$devshardAdminApiKey'" +
            " DEVSHARD_STORAGE_DIR=$gatewayStorageDir" +
            " DEVSHARD_PORT=$gatewayPort" +
            " DEVSHARD_CHAIN_GRPC=\$NODE_HOST:$chainGrpcPort" +
            " DEVSHARD_CHAIN_RPC=http://\$NODE_HOST:$chainRpcPort" +
            " DEVSHARD_PUBLIC_API=http://localhost:$publicApiPort" +
            " NODE_MANAGER_ADDR=localhost:$nodeManagerPort" +
            // Tiny test PoC weight rounds the weight-derived cap to 0, so the per-weight figure is set high.
            " GATEWAY_MAX_CONCURRENT_REQUESTS=0" +
            " GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT=0" +
            " GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT=$testPocRequestsPerWeight" +
            " GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT=$testPocRequestsPerWeight" +
            // Hosts register docker-internal names that resolve to private IPs; production leaves the guard on.
            " DEVSHARD_ALLOW_PRIVATE_ADDRESSES=true" +
            " nohup $gatewayContainerBinaryPath >>$gatewayLogPath 2>&1 &" +
            " echo \$!",
    )
    api.executor.exec(startCommand, null)
    if (!waitUntil(gatewayHealthAttempts) { isGatewayHealthy() }) {
        error("gateway did not answer /healthz. Log tail:\n${gatewayLogTail()}")
    }
    state.runningSeeds = state.seeds.toMap()
    state.logFollower = GatewayLogFollower(containerId, gatewayLogPath, config.pairName)
    Logger.info("Gateway started in {} serving escrows {}", containerId, state.runningSeeds.keys)
}

private fun LocalInferencePair.stopGatewayProcess() {
    api.executor.exec(
        listOf(
            "sh", "-c",
            "pkill -TERM -x $gatewayBinaryName || exit 0; " +
                "for attempt in \$(seq 1 $gatewayStopAttempts); do " +
                "pgrep -x $gatewayBinaryName >/dev/null || exit 0; sleep ${gatewayPollIntervalMillis / 1000.0}; done; " +
                "pkill -KILL -x $gatewayBinaryName || true",
        ),
        null,
    )
}

private fun LocalInferencePair.activateEscrow(escrowId: Long) {
    var last = noGatewayAnswer
    val activated = waitUntil(gatewayActivateAttempts) {
        last = runCatching {
            gatewayAdminRequest("POST", "/v1/admin/devshards/$escrowId/activate", maxTimeSeconds = gatewayActivateTimeoutSeconds)
        }.getOrElse { failure -> noGatewayAnswer.copy(body = failure.message.orEmpty()) }
        last.httpCode == httpStatusOk
    }
    check(activated) {
        "gateway did not activate escrow $escrowId (HTTP ${last.httpCode}: ${last.body}). Log tail:\n${gatewayLogTail()}"
    }
}

private fun LocalInferencePair.waitForEscrowRoutable(escrowId: Long) {
    val routable = waitUntil(gatewayRoutableAttempts) { gatewayAnswers("/devshard/$escrowId/v1/status") }
    check(routable) { "gateway does not route escrow $escrowId. Log tail:\n${gatewayLogTail()}" }
}

private fun LocalInferencePair.gatewayLogTail(): String = runCatching {
    api.executor.exec(listOf("tail", "-n", "$gatewayLogTailLines", gatewayLogPath), null).joinToString("\n")
}.getOrDefault("no gateway log")

private fun waitUntil(attempts: Int, condition: () -> Boolean): Boolean {
    repeat(attempts) {
        if (condition()) {
            return true
        }
        Thread.sleep(gatewayPollIntervalMillis)
    }
    return false
}
