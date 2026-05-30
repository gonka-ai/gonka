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
 */
const val DEVSHARD_VERSION_ENV = "DEVSHARD_VERSION"

const val DEVSHARD_VERSION_STAMP = "build/devshard-version"

const val DEVSHARD_OVERRIDE_BINARY_PATH = "/opt/overrides/devshardd"

private val resolvedDevshardTestVersion: String by lazy { resolveDevshardTestVersion() }

/** Version name used for VERSIOND_FORCE and /devshard/<version>/ routes. */
fun devshardTestVersion(): String = resolvedDevshardTestVersion

private fun resolveDevshardTestVersion(): String {
    System.getenv(DEVSHARD_VERSION_ENV)?.takeIf { it.isNotBlank() }?.let { return it }
    readDevshardVersionStamp()?.let { return it }
    makefileDevshardVersion()?.let { return it }
    return "dev"
}

private fun readDevshardVersionStamp(): String? =
    try {
        val stamp = Path.of(getRepoRoot(), DEVSHARD_VERSION_STAMP)
        if (!Files.isRegularFile(stamp)) {
            null
        } else {
            Files.readString(stamp).trim().takeIf { it.isNotBlank() }
        }
    } catch (_: Exception) {
        null
    }

private fun makefileDevshardVersion(): String? =
    try {
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
        val out = proc.inputStream.bufferedReader().readText().trim()
        if (proc.waitFor() == 0 && out.isNotBlank()) out else null
    } catch (_: Exception) {
        null
    }

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
