import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.tinylog.kotlin.Logger
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption
import java.security.MessageDigest
import java.util.zip.CRC32
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/**
 * Shared fixtures for versiond → devshardd Testermint suites.
 *
 * Override-driven tests use [versiondOverrideEnv] / [VERSIOND_FORCE] so each pair
 * runs the locally built binary. State-driven download tests seed
 * `approved_versions` and omit FORCE/OVERRIDE.
 */
abstract class DevshardVersiondTestBase : TestermintTest() {
    protected val versiondTestVersionName = devshardTestVersion()
    protected val devshardEscrowModel = defaultModel

    protected data class PreparedDevsharddArtifact(
        val approvedVersion: DevshardApprovedVersion,
    ) {
        val routePrefix: String
            get() = "/devshard/${approvedVersion.name}"
    }

    // Default join count is 2 → three pairs (genesis, join1, join2).
    protected val versiondComposeFilesByPairName = listOf(GENESIS_KEY_NAME, "join1", "join2")
        .associateWith { listOf("docker-compose.versiond.yml") }

    protected val overrideVersiondEnv = versiondOverrideEnv(versiondTestVersionName)

    protected val stateDrivenVersiondEnv = mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

    protected val overrideRoutePrefix = com.productscience.devshardVersionedRoutePrefix(versiondTestVersionName)

    private val devsharddArtifactDockerUrl =
        "http://${GENESIS_KEY_NAME}-devshardd-artifact-server:8080/devshardd.zip"
    private val devsharddArtifactShaUrl = "$devsharddArtifactDockerUrl.sha256"
    private val repoRoot: Path by lazy { resolveRepoRoot() }
    private val devsharddHostBinary: Path
        get() = repoRoot.resolve("build").resolve("devshardd")
    private val devsharddArtifactDir: Path
        get() = repoRoot.resolve("build").resolve("devshardd-artifacts")
    private val devsharddArtifactZip = devsharddArtifactDir.resolve("devshardd.zip")
    private val devsharddArtifactSha = devsharddArtifactDir.resolve("devshardd.zip.sha256")

    protected val overrideConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec),
        env = overrideVersiondEnv,
    )

    protected val streamingLongEpochConfig = versiondConfig(
        genesisSpec = createSpec(epochLength = 20, epochShift = 10).merge(devshardNoRestrictionsSpec),
        env = overrideVersiondEnv,
    )

    protected val parallelLongEpochConfig = versiondConfig(
        genesisSpec = createSpec(epochLength = 25, epochShift = 10)
            .merge(devshardNoRestrictionsSpec)
            // Escrow sampling (bps); 50% so each parallel session is likely to
            // record validation observability before the wait times out.
            .merge(devshardEscrowHalfValidateSpec),
        env = overrideVersiondEnv,
    )

    protected val overrideAlwaysValidateConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(
            devshardNoRestrictionsSpec,
            devshardAlwaysValidateSpec,
            // Escrow sampling is independent of legacy ValidationParams; 50% so the
            // missing-logit executor is challenged often enough for the assert.
            devshardEscrowHalfValidateSpec,
        ),
        env = overrideVersiondEnv,
    )

    protected val shortSealGraceConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec, devshardShortSealGraceSpec),
        env = overrideVersiondEnv,
    )

    protected fun versiondConfig(
        genesisSpec: Spec<AppState>,
        env: Map<String, String>,
    ): ApplicationConfig = inferenceConfig.copy(
        genesisSpec = genesisSpec,
        additionalDockerFilesByKeyName = versiondComposeFilesByPairName,
        additionalEnvVars = env,
    )

    protected fun mergedGenesisSpec(vararg specs: Spec<AppState>): Spec<AppState> {
        val base = inferenceConfig.genesisSpec ?: spec<AppState> {}
        return specs.fold(base) { current, extra -> current.merge(extra) }
    }

    protected fun stateDrivenConfig(approvedVersion: DevshardApprovedVersion): ApplicationConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(
            devshardNoRestrictionsSpec,
            approvedVersionsSpec(listOf(approvedVersion)),
        ),
        env = stateDrivenVersiondEnv,
    )

    protected fun approvedVersionsSpec(
        versions: List<DevshardApprovedVersion>,
    ): Spec<AppState> = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                    this[DevshardEscrowParams::approvedVersions] = versions
                }
            }
        }
    }

    protected fun assertNoVersiondOverrides(env: Map<String, String>) {
        assertThat(env).doesNotContainKey("VERSIOND_FORCE")
        assertThat(env.keys.none { it.startsWith("VERSIOND_OVERRIDE_") }).isTrue()
    }

    protected fun prepareReleaseStyleDevsharddArtifact(
        versionName: String = versiondTestVersionName,
    ): PreparedDevsharddArtifact {
        check(Files.isRegularFile(devsharddHostBinary)) {
            "Missing devshardd binary at $devsharddHostBinary. Build it before running this test."
        }

        Files.createDirectories(devsharddArtifactDir)

        writeDeterministicZip(
            sourceBinary = devsharddHostBinary,
            targetZip = devsharddArtifactZip,
            binaryName = "devshardd",
        )
        val archiveSha256 = sha256Hex(devsharddArtifactZip)
        Files.writeString(devsharddArtifactSha, archiveSha256)

        Logger.info("Prepared devshardd release artifact: version=$versionName sha256=$archiveSha256")

        return PreparedDevsharddArtifact(
            approvedVersion = DevshardApprovedVersion(
                name = versionName,
                binary = devsharddArtifactDockerUrl,
                sha256 = archiveSha256,
            ),
        )
    }

    protected fun waitForDevsharddArtifactSha(genesis: LocalInferencePair): String {
        var sha256: String? = null
        waitUntil("devshardd artifact server", timeoutSeconds = 120) {
            try {
                sha256 = genesis.curlFromApiNetwork(devsharddArtifactShaUrl).takeIf { it.isNotBlank() }
                sha256 != null
            } catch (e: Exception) {
                Logger.debug("devshardd artifact server not ready: ${e.message}")
                false
            }
        }
        return requireNotNull(sha256) { "devshardd artifact server did not become ready" }
    }

    private fun writeDeterministicZip(sourceBinary: Path, targetZip: Path, binaryName: String) {
        val binaryMetadata = hashAndCrc32(sourceBinary)
        Files.newOutputStream(
            targetZip,
            StandardOpenOption.CREATE,
            StandardOpenOption.TRUNCATE_EXISTING,
            StandardOpenOption.WRITE,
        ).buffered().use { output ->
            ZipOutputStream(output).use { zip ->
                val entry = ZipEntry(binaryName).apply {
                    method = ZipEntry.STORED
                    time = 946684800000L // 2000-01-01T00:00:00Z
                    size = binaryMetadata.size
                    compressedSize = binaryMetadata.size
                    crc = binaryMetadata.crc32
                }
                zip.putNextEntry(entry)
                Files.newInputStream(sourceBinary).buffered().use { input ->
                    input.copyTo(zip)
                }
                zip.closeEntry()
            }
        }
    }

    private data class StreamHashMetadata(
        val size: Long,
        val crc32: Long,
        val sha256: String,
    )

    private fun hashAndCrc32(path: Path): StreamHashMetadata {
        val digest = MessageDigest.getInstance("SHA-256")
        val crc32 = CRC32()
        var size = 0L
        val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
        Files.newInputStream(path).buffered().use { input ->
            while (true) {
                val read = input.read(buffer)
                if (read < 0) break
                digest.update(buffer, 0, read)
                crc32.update(buffer, 0, read)
                size += read.toLong()
            }
        }
        return StreamHashMetadata(
            size = size,
            crc32 = crc32.value,
            sha256 = digest.digest().joinToString("") { "%02x".format(it) },
        )
    }

    private fun sha256Hex(path: Path): String = hashAndCrc32(path).sha256

    private fun resolveRepoRoot(): Path {
        val cwd = Path.of("").toAbsolutePath().normalize()
        return generateSequence(cwd) { it.parent }
            .firstOrNull { candidate ->
                Files.isDirectory(candidate.resolve("testermint")) &&
                    Files.isDirectory(candidate.resolve("local-test-net")) &&
                    Files.isDirectory(candidate.resolve("versioned"))
            }
            ?: error("Repository root not found from $cwd")
    }

    @Suppress("UNCHECKED_CAST")
    protected fun getDapiVersions(genesis: LocalInferencePair): List<Map<String, Any>> {
        return try {
            val mlUrl = genesis.api.urls[SERVER_TYPE_ML] ?: return emptyList()
            val (_, _, result) = Fuel.get("$mlUrl/versions")
                .timeoutRead(5000)
                .responseString()
            val body = result.get()
            val parsed = cosmosJson.fromJson(body, Map::class.java)
            (parsed["versions"] as? List<Map<String, Any>>) ?: emptyList()
        } catch (e: Exception) {
            Logger.warn("Failed to query dapi /versions: ${e.message}")
            emptyList()
        }
    }

    protected fun getVersionedHealth(genesis: LocalInferencePair, versionName: String): String {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/healthz")
            .timeoutRead(10000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/healthz returned ${response.statusCode}: $result")
            .isEqualTo(200)
        return result.get().trim()
    }

    protected fun waitForOverrideVersionedHealth(
        genesis: LocalInferencePair,
        versionName: String = versiondTestVersionName,
    ) {
        waitUntil("proxy serves /devshard/$versionName/healthz", timeoutSeconds = 90) {
            runCatching { getVersionedHealth(genesis, versionName) == "ok" }.getOrDefault(false)
        }
    }

    protected fun waitUntil(description: String, timeoutSeconds: Int, condition: () -> Boolean) {
        val deadline = System.currentTimeMillis() + timeoutSeconds * 1000L
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return
            Thread.sleep(2000)
        }
        error("Timed out waiting for: $description (${timeoutSeconds}s)")
    }
}
