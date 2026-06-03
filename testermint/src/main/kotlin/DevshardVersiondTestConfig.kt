package com.productscience

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
