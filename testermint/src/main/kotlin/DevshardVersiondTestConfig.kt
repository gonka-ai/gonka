package com.productscience

import com.github.kittinunf.fuel.Fuel
import org.tinylog.kotlin.Logger
import java.nio.file.Files
import java.nio.file.Path

/**
 * Shared devshardd/versiond naming for Testermint override tests.
 *
 * Resolution order for [devshardTestVersion] (must match `make devshardd-build`):
 * 1. `DEVSHARD_VERSION` env (explicit override for CI or one-off runs)
 * 2. `build/devshard-version` (written by `make devshardd-build`)
 * 3. `make -C <repo> print-devshard-version` (root Makefile `DEVSHARD_VERSION`, default `dev`)
 *
 * State-root / settlement protocol tag ([devshardStateRootProtocolVersion]) is separate from
 * versiond runtime name. It is baked into devshardd via link flags; Testermint reads
 * `build/devshard-protocol-version` (must match `make devshardd-build` / `DEVSHARD_PROTOCOL_VERSION`).
 */
const val DEVSHARD_VERSION_ENV = "DEVSHARD_VERSION"

const val DEVSHARD_VERSION_STAMP = "build/devshard-version"

const val DEVSHARD_PROTOCOL_VERSION_STAMP = "build/devshard-protocol-version"

const val DEVSHARD_OVERRIDE_BINARY_PATH = "/opt/overrides/devshardd"

private val resolvedDevshardTestVersion: String by lazy { resolveDevshardTestVersion() }

private val resolvedDevshardProtocolVersion: String by lazy { resolveDevshardProtocolVersion() }

/** Version name used for VERSIOND_FORCE and /devshard/<version>/ routes. */
fun devshardTestVersion(): String = resolvedDevshardTestVersion

/** State-root / settlement protocol tag for finalize and on-chain settlement. */
fun devshardStateRootProtocolVersion(): String = resolvedDevshardProtocolVersion

private fun resolveDevshardTestVersion(): String {
    System.getenv(DEVSHARD_VERSION_ENV)?.takeIf { it.isNotBlank() }?.let { return it }
    readDevshardVersionStamp()?.let { return it }
    makefileDevshardVersion()?.let { return it }
    return "dev"
}

private fun resolveDevshardProtocolVersion(): String {
    readDevshardProtocolVersionStamp()?.let { return it }
    makefileDevshardProtocolVersion()?.let { return it }
    return "v2"
}

private fun readDevshardVersionStamp(): String? = runCatching {
    val stamp = Path.of(getRepoRoot(), DEVSHARD_VERSION_STAMP)
    if (!Files.isRegularFile(stamp)) {
        return@runCatching null
    }
    Files.readString(stamp).trim().takeIf { it.isNotBlank() }
}.getOrNull()

private fun readDevshardProtocolVersionStamp(): String? = runCatching {
    val stamp = Path.of(getRepoRoot(), DEVSHARD_PROTOCOL_VERSION_STAMP)
    if (!Files.isRegularFile(stamp)) {
        return@runCatching null
    }
    Files.readString(stamp).trim().takeIf { it.isNotBlank() }
}.getOrNull()

private fun makefileDevshardVersion(): String? = runCatching {
    val proc =
        ProcessBuilder(
            "make",
            "-s",
            "--no-print-directory",
            "-C",
            getRepoRoot(),
            "print-devshard-version",
        )
            .redirectErrorStream(true)
            .start()
    val out = proc.inputStream.bufferedReader().use { it.readText().trim() }
    if (proc.waitFor() == 0 && out.isNotBlank()) out else null
}.getOrNull()

private fun makefileDevshardProtocolVersion(): String? = runCatching {
    val proc =
        ProcessBuilder(
            "make",
            "-s",
            "--no-print-directory",
            "-C",
            getRepoRoot(),
            "print-devshard-protocol-version",
        )
            .redirectErrorStream(true)
            .start()
    val out = proc.inputStream.bufferedReader().use { it.readText().trim() }
    if (proc.waitFor() == 0 && out.isNotBlank()) out else null
}.getOrNull()

/** Maps version name to VERSIOND_OVERRIDE env suffix (dots -> underscores). */
fun versiondOverrideEnvKey(version: String): String =
    "VERSIOND_OVERRIDE_${version.replace('.', '_')}"

/** Env vars for versiond compose: force local override binary as [version]. */
fun versiondOverrideEnv(version: String = devshardTestVersion()): Map<String, String> =
    mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        versiondOverrideEnvKey(version) to DEVSHARD_OVERRIDE_BINARY_PATH,
        "VERSIOND_FORCE" to version,
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

fun devshardVersionedRoutePrefix(version: String = devshardTestVersion()): String =
    "/devshard/$version"

/**
 * [local-test-net/docker-compose.versiond.yml] only declares `VERSIOND_OVERRIDE_dev` for
 * compose substitution. Other version names need an explicit service env entry.
 */
fun warnIfComposeOverrideKeyNotDeclared(version: String, pairName: String) {
    if (version == "dev") {
        return
    }
    Logger.warn(
        "[{}] versiond compose file declares VERSIOND_OVERRIDE_dev only; " +
            "override tests using version '{}' need VERSIOND_OVERRIDE_{} in docker-compose.versiond.yml",
        pairName,
        version,
        version.replace('.', '_'),
    )
}

fun LocalInferencePair.queryVersionedHealth(versionName: String): String? =
    runCatching {
        val url = "${api.getPublicUrl()}/devshard/$versionName/healthz"
        val (_, response, result) = Fuel.get(url).timeoutRead(10_000).responseString()
        "${response.statusCode}:${result.get().trim().take(200)}"
    }.getOrNull()

/** Read versiond container env and recent logs (for JUnit / local failure diagnosis). */
fun LocalInferencePair.logVersiondDiagnostics(expectedVersion: String, logTail: Int = 120) {
    val pairLabel = name.trimStart('/')
    Logger.info("[{}] versiond runtime diagnostics (expectedVersion={})", pairLabel, expectedVersion)
    try {
        val envLines =
            execInVersiond(
                listOf("sh", "-c", "env | grep -E '^VERSIOND_' | sort"),
                null,
            ).joinToString("\n")
        if (envLines.isBlank()) {
            Logger.warn("[{}]   versiond container: no VERSIOND_* env vars visible", pairLabel)
        } else {
            envLines.lineSequence().forEach { line ->
                Logger.info("[{}]   versiond env {}", pairLabel, line)
            }
        }
    } catch (e: Exception) {
        Logger.warn("[{}]   could not read versiond env: {}", pairLabel, e.message)
    }

    try {
        val overrideMount =
            execInVersiond(
                listOf(
                    "sh",
                    "-c",
                    "ls -la '$DEVSHARD_OVERRIDE_BINARY_PATH' 2>&1 || echo MISSING",
                ),
                null,
            ).joinToString(" ").trim()
        Logger.info("[{}]   override mount {}: {}", pairLabel, DEVSHARD_OVERRIDE_BINARY_PATH, overrideMount)
    } catch (e: Exception) {
        Logger.warn("[{}]   override mount check failed: {}", pairLabel, e.message)
    }

    val binExists = versiondBinaryExists(expectedVersion, "devshardd")
    Logger.info(
        "[{}]   installed binary {} exists={}",
        pairLabel,
        versiondBinaryPath(expectedVersion, "devshardd"),
        binExists,
    )

    val health = queryVersionedHealth(expectedVersion)
    Logger.info("[{}]   GET /devshard/{}/healthz => {}", pairLabel, expectedVersion, health ?: "<unavailable>")

    try {
        val logs = readVersiondLogs(tail = logTail)
        val interesting =
            logs.lineSequence()
                .filter { line ->
                    line.contains("versiond", ignoreCase = true) ||
                        line.contains("VERSIOND", ignoreCase = true) ||
                        line.contains(expectedVersion, ignoreCase = true) ||
                        line.contains("override", ignoreCase = true) ||
                        line.contains("reconcile", ignoreCase = true) ||
                        line.contains("not found", ignoreCase = true) ||
                        line.contains("error", ignoreCase = true)
                }
                .take(40)
                .joinToString("\n")
        if (interesting.isNotBlank()) {
            Logger.info("[{}]   versiond log highlights:\n{}", pairLabel, interesting)
        }
    } catch (e: Exception) {
        Logger.warn("[{}]   could not read versiond logs: {}", pairLabel, e.message)
    }
}

/**
 * Waits until versiond has installed the forced override and the proxy health route responds.
 */
fun LocalInferencePair.waitForVersiondOverrideReady(
    expectedVersion: String = devshardTestVersion(),
    timeoutSeconds: Int = 120,
) {
    logSection("Waiting for versiond override version=$expectedVersion")
    warnIfComposeOverrideKeyNotDeclared(expectedVersion, name.trimStart('/'))
    logVersiondDiagnostics(expectedVersion)
    val deadline = System.currentTimeMillis() + timeoutSeconds * 1000L
    var lastLogMs = 0L
    while (System.currentTimeMillis() < deadline) {
        val binReady = versiondBinaryExists(expectedVersion, "devshardd")
        val healthOk =
            runCatching {
                queryVersionedHealth(expectedVersion)?.startsWith("200:ok") == true
            }.getOrDefault(false)
        if (binReady && healthOk) {
            Logger.info(
                "[{}] versiond override ready (version={}, binary+healthz ok)",
                name.trimStart('/'),
                expectedVersion,
            )
            return
        }
        val now = System.currentTimeMillis()
        if (now - lastLogMs >= 30_000) {
            Logger.info(
                "[{}] still waiting for versiond override (binary={}, healthz={})",
                name.trimStart('/'),
                binReady,
                healthOk,
            )
            logVersiondDiagnostics(expectedVersion, logTail = 80)
            lastLogMs = now
        }
        Thread.sleep(2_000)
    }
    logVersiondDiagnostics(expectedVersion, logTail = 400)
    error(
        "versiond override not ready for version '$expectedVersion' within ${timeoutSeconds}s " +
            "(see versiond diagnostics above)",
    )
}
