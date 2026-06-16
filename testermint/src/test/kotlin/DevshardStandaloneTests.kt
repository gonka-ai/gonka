import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption
import java.security.MessageDigest
import java.time.Duration
import java.util.concurrent.Executors
import java.util.zip.CRC32
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream
import kotlin.test.assertNotNull

/**
 * Mirror of DevshardTests but routed through versiond -> devshardd instead of
 * dapi's in-process HostManager. The shape of every assertion is the same;
 * only the test setup differs:
 *
 *  - docker-compose.versiond.yml is included for every pair so each pair runs
 *    a versiond container that boots the locally-built devshardd binary as
 *    version "v1" (via VERSIOND_OVERRIDE_v1 + VERSIOND_FORCE).
 *  - VERSIOND_SERVICE_NAME=versiond is exported so each pair's proxy emits a
 *    /devshard/ -> versiond_backend location.
 *  - startDevshardProxy is launched with routePrefix="/devshard/v1" so
 *    devshardctl builds host URLs as proxy/devshard/v1/sessions/:id/...
 *    nginx strips /devshard/, versiond strips /v1/, devshardd handles
 *    /sessions/:id/...
 *  - DAPI's in-process HostManager is still mounted on /v1/devshard for the
 *    legacy path; the new test does not exercise it.
 *  - This file also contains a startup-seeded state-driven test for the normal
 *    `approved_versions -> /versions -> versiond download` path without local
 *    overrides for the tested version.
 */
class DevshardStandaloneTests : TestermintTest() {
    private val standaloneTestVersionName = "v1"
    private val devshardEscrowModel = defaultModel

    private data class PreparedDevsharddArtifact(
        val approvedVersion: DevshardApprovedVersion,
    ) {
        val routePrefix: String
            get() = "/devshard/${approvedVersion.name}"
    }

    // The default join count is 2 -> three pairs total (genesis, join1, join2).
    // Every pair gets the versiond compose extension so each runs its own
    // devshardd child managed by its own ${KEY_NAME}-versiond container.
    private val versiondComposeFilesByPairName = listOf(GENESIS_KEY_NAME, "join1", "join2")
        .associateWith { listOf("docker-compose.versiond.yml") }

    // Switches the test cluster from "default" to "devshardd via versiond":
    //  - VERSIOND_BINARY_NAME selects the binary versiond launches per child
    //  - VERSIOND_OVERRIDE_v1 points at the bind-mounted host binary
    //  - VERSIOND_FORCE makes versiond run that version even though it is
    //    not in the chain's approved_versions list
    //  - VERSIOND_SERVICE_NAME enables the proxy's /devshard/ -> versiond
    //    upstream block
    private val overrideVersiondEnv = mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        "VERSIOND_OVERRIDE_v1" to "/opt/overrides/devshardd",
        "VERSIOND_FORCE" to standaloneTestVersionName,
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

    private val stateDrivenVersiondEnv = mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

    private val overrideRoutePrefix = "/devshard/$standaloneTestVersionName"
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

    private val overrideConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec),
        env = overrideVersiondEnv,
    )

    private val overrideLongEpochConfig = versiondConfig(
        genesisSpec = createSpec(epochLength = 40, epochShift = 10).merge(devshardNoRestrictionsSpec),
        env = overrideVersiondEnv,
    )

    private val overrideAlwaysValidateConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec, devshardAlwaysValidateSpec),
        env = overrideVersiondEnv,
    )

    @Test
    fun `create devshard escrow and query it`() {
        val (_, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()

        val creator = genesis.node.getColdAddress()
        val initialBalance = genesis.getBalance(creator)

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L
        val txResponse = genesis.createDevshardEscrow(escrowAmount, modelId = devshardEscrowModel)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Querying devshard escrow")
        val escrowResponse = genesis.node.queryDevshardEscrow(1)
        assertThat(escrowResponse.found).isTrue()
        assertThat(escrowResponse.escrow).isNotNull()
        assertThat(escrowResponse.escrow!!.creator).isEqualTo(creator)
        assertThat(escrowResponse.escrow!!.amount).isEqualTo(escrowAmount.toString())
        assertThat(escrowResponse.escrow!!.slots).hasSize(16)
        assertThat(escrowResponse.escrow!!.settled).isFalse()

        logSection("Verifying balance decreased")
        val balanceAfter = genesis.getBalance(creator)
        assertThat(balanceAfter).isEqualTo(initialBalance - escrowAmount)
    }

    @Test
    fun `state-approved devshardd seeded at startup is downloaded without overrides`() {
        assertNoVersiondOverrides(stateDrivenVersiondEnv)

        val preparedArtifact = prepareReleaseStyleDevsharddArtifact()
        val (cluster, genesis) = initCluster(
            config = stateDrivenConfig(preparedArtifact.approvedVersion),
            reboot = true,
        )
        genesis.waitForNextEpoch()

        logSection("Waiting for devshardd artifact server readiness")
        val servedSha256 = waitForDevsharddArtifactSha(genesis)
        assertThat(servedSha256).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Verifying chain params contain ${preparedArtifact.approvedVersion.name} at startup")
        val approvedVersions = genesis.getParams().devshardEscrowParams?.approvedVersions ?: emptyList()
        val approvedVersion = approvedVersions.single { it.name == preparedArtifact.approvedVersion.name }
        assertThat(approvedVersion.binary).isEqualTo(preparedArtifact.approvedVersion.binary)
        assertThat(approvedVersion.sha256).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Waiting for dapi /versions to expose ${preparedArtifact.approvedVersion.name}")
        waitUntil("dapi serves ${preparedArtifact.approvedVersion.name}", timeoutSeconds = 60) {
            getDapiVersions(genesis).any { it["name"] == preparedArtifact.approvedVersion.name }
        }
        val dapiVersion = getDapiVersions(genesis).single { it["name"] == preparedArtifact.approvedVersion.name }
        assertThat(dapiVersion["binary"]).isEqualTo(preparedArtifact.approvedVersion.binary)
        assertThat(dapiVersion["sha256"]).isEqualTo(preparedArtifact.approvedVersion.sha256)

        logSection("Waiting for every pair to download ${preparedArtifact.approvedVersion.name}")
        waitUntil("downloaded binary and install metadata exist on every pair", timeoutSeconds = 120) {
            cluster.allPairs.all { pair ->
                pair.versiondBinaryExists(preparedArtifact.approvedVersion.name, "devshardd") &&
                        pair.readVersiondInstallMetadata(preparedArtifact.approvedVersion.name)?.archiveSha256 ==
                        preparedArtifact.approvedVersion.sha256
            }
        }
        cluster.allPairs.forEach { pair ->
            assertThat(pair.versiondBinaryExists(preparedArtifact.approvedVersion.name, "devshardd"))
                .withFailMessage(
                    "Expected ${pair.name} versiond to download " +
                            pair.versiondBinaryPath(preparedArtifact.approvedVersion.name, "devshardd")
                )
                .isTrue()
            val installMetadata = assertNotNull(pair.readVersiondInstallMetadata(preparedArtifact.approvedVersion.name))
            assertThat(installMetadata.archiveSha256).isEqualTo(preparedArtifact.approvedVersion.sha256)
            assertThat(installMetadata.binarySha256).isNotBlank()
        }

        logSection("Waiting for another versiond poll cycle to confirm stability")
        Thread.sleep(Duration.ofSeconds(7).toMillis())
        cluster.allPairs.forEach { pair ->
            val stableLogs = pair.readVersiondLogs(tail = 800)
            assertThat(stableLogs)
                .withFailMessage("Expected ${pair.name} versiond logs to stay stable after download.\n$stableLogs")
                .doesNotContain("hash mismatch on running version")
                .doesNotContain("installed archive hash mismatch")
                .doesNotContain("installed binary hash mismatch")
        }

        logSection("Verifying versioned health route through proxy")
        waitUntil("proxy serves ${preparedArtifact.routePrefix}/healthz", timeoutSeconds = 90) {
            runCatching {
                getVersionedHealth(genesis, preparedArtifact.approvedVersion.name) == "ok"
            }.getOrDefault(false)
        }
        assertThat(getVersionedHealth(genesis, preparedArtifact.approvedVersion.name)).isEqualTo("ok")
    }

    @Test
    fun `devshard inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshardd-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = false,
                expectedVersion = standaloneTestVersionName,
            )
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `devshard streaming inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = overrideConfig, reboot = true)
        genesis.waitForNextEpoch()
        waitForOverrideVersionedHealth(genesis)

        cluster.stubDevshardChatResponse(content = "hello from stream", streamDelay = Duration.ofMillis(50))

        val user = genesis.createFundedDevshardUser("devshardd-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending streaming chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response =
                    genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i", stream = true)
                assertThat(response).isNotEmpty()
                assertThat(response).contains("data:")
            }

            val result = genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = false,
                expectedVersion = standaloneTestVersionName,
            )

            logSection("Verifying inference statuses")
            val finished = genesis.getDevshardProxyInferences(handle.proxyUrl)
                .values.count { it.status == DevshardInferenceStatus.FINISHED }
            assertThat(finished)
                .describedAs("finished devshardd inferences")
                .isGreaterThanOrEqualTo(numInferences.toInt())
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `parallel devshard sessions with isolated settlement via devshardd`() {
        val sessionCount = 6
        val (cluster, genesis) = initCluster(config = overrideLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        data class UserInfo(val keyName: String, val address: String)
        data class SessionSetup(val keyName: String, val address: String, val escrowId: Long)

        val fundAmount = 10_000_000_000L
        val escrowAmount = 7_000_000_000L

        val users = (0 until sessionCount).map { i ->
            val user = genesis.createFundedDevshardUser("devshardd-parallel-$i", fundAmount)
            UserInfo(user.keyName, user.address)
        }

        genesis.waitForNextEpoch()
        genesis.waitForNextInferenceWindow()

        val sessions = users.mapIndexed { i, user ->
            logSection("Creating escrow for user $i")
            val escrowId =
                genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
            SessionSetup(user.keyName, user.address, escrowId)
        }

        logSection("Starting $sessionCount devshard proxies against devshardd")
        val handles = sessions.map { session ->
            genesis.startDevshardProxy(
                escrowId = session.escrowId,
                keyName = session.keyName,
                routePrefix = overrideRoutePrefix,
            )
        }

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Running $sessionCount proxy sessions in parallel")
            val dispatcher = Executors.newFixedThreadPool(sessionCount).asCoroutineDispatcher()
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }

            logSection("Finalizing, settling, and verifying $sessionCount escrows")
            sessions.zip(handles).forEach { (session, handle) ->
                val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
                assertThat(result.parsed.escrowId)
                    .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                    .isEqualTo(session.escrowId.toString())
                assertThat(result.parsed.version).isEqualTo(standaloneTestVersionName)
                assertThat(result.parsed.hostStats).isNotEmpty()
                assertThat(result.parsed.signatures).isNotEmpty()
                assertThat(result.parsed.hostStats.sumOf { it.completedValidations }).isGreaterThan(0)

                val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = session.keyName)
                assertThat(settleResp.code)
                    .withFailMessage("Settlement failed for escrow ${session.escrowId}")
                    .isEqualTo(0)
                val settleEvent = assertNotNull(settleResp.events.firstOrNull { it.type == "devshard_escrow_settled" })
                assertThat(settleEvent.attributes.firstOrNull { it.key == "version" }?.value)
                    .isEqualTo(standaloneTestVersionName)

                val escrow = genesis.node.queryDevshardEscrow(session.escrowId)
                assertThat(escrow.escrow!!.settled)
                    .withFailMessage("Escrow ${session.escrowId} not settled")
                    .isTrue()

                val balance = genesis.getBalance(session.address)
                assertThat(balance)
                    .withFailMessage("User ${session.keyName} did not receive refund")
                    .isGreaterThan(fundAmount - escrowAmount)
            }
        } finally {
            handles.forEach { genesis.stopDevshardProxy(it.escrowId) }
        }
    }

    @Test
    fun `invalid inference is challenged via devshardd`() {
        val (cluster, genesis) = initCluster(config = overrideAlwaysValidateConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(
                response = defaultInferenceResponseObject,
                streamDelay = Duration.ofMillis(50),
            )
        }
        cluster.allPairs.last().mock?.stubDevshardResponseForAllSegments(
            response = defaultInferenceResponseObject.withMissingLogit(),
        )

        val user = genesis.createFundedDevshardUser("devshardd-challenged-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.waitForDevshardPreFinalize()
            logSection("Finalizing via proxy")
            val result = genesis.finalizeDevshardProxy(handle.proxyUrl)

            logSection("Verifying settlement data")
            assertThat(result.parsed.escrowId).isEqualTo("$escrowId")
            assertThat(result.parsed.version).isEqualTo(standaloneTestVersionName)
            assertThat(result.parsed.nonce).isGreaterThan(0)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()

            logSection("Submitting settlement from user account")
            val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = user.keyName)
            assertThat(settleResp.code).isEqualTo(0)
            val settleEvent = assertNotNull(settleResp.events.firstOrNull { it.type == "devshard_escrow_settled" })
            assertThat(settleEvent.attributes.firstOrNull { it.key == "version" }?.value)
                .isEqualTo(standaloneTestVersionName)

            logSection("Verifying escrow settled")
            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            assertThat(escrow.escrow!!.settled).isTrue()

            logSection("Verifying inference status")
            val inference = assertNotNull(genesis.findChallengedDevshardInference(handle))
            logSection("Inference: $inference")
            assertThat(inference.status).isEqualTo(DevshardInferenceStatus.CHALLENGED)
            assertThat(inference.votesInvalid).isNotZero()
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    private fun versiondConfig(
        genesisSpec: Spec<AppState>,
        env: Map<String, String>,
    ): ApplicationConfig = inferenceConfig.copy(
        genesisSpec = genesisSpec,
        additionalDockerFilesByKeyName = versiondComposeFilesByPairName,
        additionalEnvVars = env,
    )

    private fun mergedGenesisSpec(vararg specs: Spec<AppState>): Spec<AppState> {
        val base = inferenceConfig.genesisSpec ?: spec<AppState> {}
        return specs.fold(base) { current, extra -> current.merge(extra) }
    }

    private fun stateDrivenConfig(approvedVersion: DevshardApprovedVersion): ApplicationConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(
            devshardNoRestrictionsSpec,
            approvedVersionsSpec(listOf(approvedVersion)),
        ),
        env = stateDrivenVersiondEnv,
    )

    private fun approvedVersionsSpec(
        versions: List<DevshardApprovedVersion>
    ): Spec<AppState> = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                    this[DevshardEscrowParams::approvedVersions] = versions
                }
            }
        }
    }

    private fun assertNoVersiondOverrides(env: Map<String, String>) {
        assertThat(env).doesNotContainKey("VERSIOND_FORCE")
        assertThat(env.keys.none { it.startsWith("VERSIOND_OVERRIDE_") }).isTrue()
    }

    private fun prepareReleaseStyleDevsharddArtifact(
        versionName: String = standaloneTestVersionName,
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
            )
        )
    }

    private fun waitForDevsharddArtifactSha(genesis: LocalInferencePair): String {
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
    private fun getDapiVersions(genesis: LocalInferencePair): List<Map<String, Any>> {
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

    private fun getVersionedHealth(genesis: LocalInferencePair, versionName: String): String {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/healthz")
            .timeoutRead(10000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/healthz returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return result.get().trim()
    }

    private fun waitForOverrideVersionedHealth(
        genesis: LocalInferencePair,
        versionName: String = standaloneTestVersionName,
    ) {
        waitUntil("proxy serves /devshard/$versionName/healthz", timeoutSeconds = 90) {
            runCatching { getVersionedHealth(genesis, versionName) == "ok" }.getOrDefault(false)
        }
    }

    private fun waitUntil(description: String, timeoutSeconds: Int, condition: () -> Boolean) {
        val deadline = System.currentTimeMillis() + timeoutSeconds * 1000L
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return
            Thread.sleep(2000)
        }
        error("Timed out waiting for: $description (${timeoutSeconds}s)")
    }
}
