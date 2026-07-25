import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.*
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.util.concurrent.CompletableFuture
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Full-circle E2E tests for versiond:
 *   1. Local chain + versiond container
 *   2. Verify empty approved_versions on startup
 *   3. Governance proposal adds a devshard binary version
 *   4. versiond downloads the binary and proxies traffic
 *   5. Second proposal adds another version, both route correctly
 *   6. Same-version binary update drains old requests while new traffic is routed
 *
 * Approved version names must match each binary's --print-protocol-version
 * (see versioned/e2e/testapp: "testapp" and "testapp2").
 *
 * Requires docker-compose.versiond.yml (adds versiond + testapp-server services).
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
@Timeout(value = 10, unit = TimeUnit.MINUTES)
class VersiondTests : TestermintTest() {

    private val testappServerUrl = "http://localhost:$TESTAPP_SERVER_HOST_PORT"
    private val dapiMlUrl = "http://localhost:$DAPI_ML_HOST_PORT"
    private val testappBinaryDockerUrl =
        "http://${GENESIS_KEY_NAME}-testapp-server:8080/testapp.zip"
    private val testapp2BinaryDockerUrl =
        "http://${GENESIS_KEY_NAME}-testapp-server:8080/testapp2.zip"
    private val testappRolloutBinaryDockerUrl =
        "http://${GENESIS_KEY_NAME}-testapp-server:8080/testapp-rollout.zip"

    private lateinit var cluster: LocalCluster
    private lateinit var genesis: LocalInferencePair
    private lateinit var testappSha256: String
    private lateinit var testapp2Sha256: String
    private lateinit var testappRolloutSha256: String

    @BeforeAll
    fun setup() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName = mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose.versiond.yml")
            ),
            additionalEnvVars = mapOf(
                "VERSIOND_SERVICE_NAME" to "versiond"
            )
        )
        val (c, g) = initCluster(config = config, reboot = true)
        cluster = c
        genesis = g

        logSection("Waiting for testapp-server readiness")
        testappSha256 = waitForTestappArtifactSha256(TESTAPP_VERSION)
        testapp2Sha256 = waitForTestappArtifactSha256(TESTAPP2_VERSION)
        testappRolloutSha256 = waitForTestappArtifactSha256(TESTAPP_ROLLOUT_ARTIFACT)
        Logger.info("testapp zip sha256: $testappSha256")
        Logger.info("testapp2 zip sha256: $testapp2Sha256")
        Logger.info("testapp-rollout zip sha256: $testappRolloutSha256")
    }

    @AfterAll
    fun teardown() {
        if (::genesis.isInitialized) {
            genesis.markNeedsReboot()
        }
    }

    @Test
    @Order(1)
    fun `approved versions empty on startup`() {
        logSection("Verifying chain params have no approved versions")
        val params = genesis.getParams()
        val approvedVersions = params.devshardEscrowParams?.approvedVersions ?: emptyList()
        assertThat(approvedVersions)
            .withFailMessage("Expected no approved versions in initial chain params")
            .isEmpty()

        logSection("Verifying dapi serves empty versions list")
        val dapiVersions = getDapiVersions()
        assertThat(dapiVersions)
            .withFailMessage("Expected dapi /versions to return empty list")
            .isEmpty()

    }

    @Test
    @Order(2)
    fun `governance proposal adds first version and versiond downloads it`() {
        val versionName = TESTAPP_VERSION

        logSection("Submitting governance proposal to add $versionName")
        val params = genesis.getParams()
        val updatedParams = params.withApprovedVersions(
            listOf(
                DevshardApprovedVersion(
                    name = versionName,
                    binary = testappBinaryDockerUrl,
                    sha256 = testappSha256,
                )
            )
        )
        genesis.runProposal(cluster, UpdateParams(params = updatedParams))

        logSection("Verifying chain params updated")
        val newParams = genesis.getParams()
        val versions = newParams.devshardEscrowParams?.approvedVersions ?: emptyList()
        assertThat(versions).hasSize(1)
        assertThat(versions[0].name).isEqualTo(versionName)
        assertThat(versions[0].sha256).isEqualTo(testappSha256)

        logSection("Waiting for dapi to serve the new version")
        waitUntil("dapi serves $versionName", timeoutSeconds = 30) {
            getDapiVersions().any { it["name"] == versionName }
        }

        logSection("Verifying proxy routing through versiond")
        val response = waitForVersionedProxy(versionName)
        assertThat(response["prefix"])
            .withFailMessage("Expected testapp to report prefix=$versionName, got ${response["prefix"]}")
            .isEqualTo(versionName)
        logHighlight("$versionName routed successfully through versiond")
    }

    @Test
    @Order(3)
    fun `subnet binary can talk to nodemanager grpc service`() {
        val versionName = TESTAPP_VERSION

        logSection("Calling /nodemanager-test through versiond proxy")
        val response = getNodeManagerTest(versionName)

        assertThat(response["nodemanager_addr"])
            .withFailMessage("Expected nodemanager_addr to be set, got: $response")
            .isNotNull()

        assertThat(response["grpc_connected"])
            .withFailMessage("Expected grpc_connected=true, got: $response")
            .isEqualTo("true")

        if (response.containsKey("endpoint") && response["endpoint"]?.isNotEmpty() == true) {
            logHighlight("Acquire succeeded: endpoint=${response["endpoint"]}, node_id=${response["node_id"]}")
            assertThat(response["lock_id"])
                .withFailMessage("Expected lock_id to be set on successful acquire")
                .isNotNull()
        } else {
            logHighlight("Acquire returned no nodes (expected in minimal test env): ${response["error"]}")
            assertThat(response["error"])
                .withFailMessage("Expected either endpoint or error in response")
                .isNotNull()
        }
    }

    @Test
    @Order(4)
    fun `governance proposal adds second version and both route`() {
        val v1 = TESTAPP_VERSION
        val v2 = TESTAPP2_VERSION

        logSection("Submitting governance proposal to add $v2 (keeping $v1)")
        val params = genesis.getParams()
        val updatedParams = params.withApprovedVersions(
            listOf(
                DevshardApprovedVersion(
                    name = v1,
                    binary = testappBinaryDockerUrl,
                    sha256 = testappSha256,
                ),
                DevshardApprovedVersion(
                    name = v2,
                    binary = testapp2BinaryDockerUrl,
                    sha256 = testapp2Sha256,
                ),
            )
        )
        genesis.runProposal(cluster, UpdateParams(params = updatedParams))

        logSection("Verifying chain params have both versions")
        val newParams = genesis.getParams()
        val versions = newParams.devshardEscrowParams?.approvedVersions ?: emptyList()
        assertThat(versions).hasSize(2)
        assertThat(versions.map { it.name }).containsExactlyInAnyOrder(v1, v2)

        logSection("Verifying $v1 still routes")
        val resp1 = waitForVersionedProxy(v1)
        assertThat(resp1["prefix"]).isEqualTo(v1)

        logSection("Verifying $v2 routes")
        val resp2 = waitForVersionedProxy(v2)
        assertThat(resp2["prefix"]).isEqualTo(v2)

        logHighlight("Both $v1 and $v2 route correctly through versiond")
    }

    @Test
    @Order(5)
    fun `same version binary update drains old requests and keeps serving`() {
        val versionName = TESTAPP_VERSION
        val oldPrefix = TESTAPP_VERSION
        val newPrefix = TESTAPP_ROLLOUT_PREFIX
        val slowDuration = Duration.ofSeconds(75)

        logSection("Verifying $versionName starts on the original binary")
        waitForVersionedProxyPrefix(versionName, oldPrefix)

        logSection("Starting a long request before updating $versionName")
        val slowRequest = CompletableFuture.supplyAsync<Map<String, Any>> {
            getVersiondProxySlow(versionName, slowDuration)
        }
        waitUntil("slow request accepted by old $versionName child", timeoutSeconds = 10) {
            runCatching { statusNumber(getDrainStatus(versionName), "inflight") > 0 }
                .getOrDefault(false)
        }

        val stopProbe = AtomicBoolean(false)
        val observedPrefixes = CopyOnWriteArrayList<String>()
        val probeFailures = CopyOnWriteArrayList<String>()
        val continuityProbe = CompletableFuture.runAsync {
            while (!stopProbe.get()) {
                try {
                    getVersiondProxy(versionName)["prefix"]?.toString()?.let(observedPrefixes::add)
                } catch (e: Exception) {
                    probeFailures.add(e.message ?: e.toString())
                }
                Thread.sleep(250)
            }
        }

        try {
            logSection("Submitting governance proposal to update $versionName to rollout sha")
            val params = genesis.getParams()
            val currentVersions = params.devshardEscrowParams?.approvedVersions ?: emptyList()
            assertThat(currentVersions.any { it.name == versionName && it.sha256 == testappSha256 })
                .withFailMessage("Expected $versionName to start on sha $testappSha256")
                .isTrue()

            val rolloutVersion = DevshardApprovedVersion(
                name = versionName,
                binary = testappRolloutBinaryDockerUrl,
                sha256 = testappRolloutSha256,
            )
            val updatedParams = params.withApprovedVersions(
                currentVersions.filterNot { it.name == versionName } + rolloutVersion
            )
            genesis.runProposal(cluster, UpdateParams(params = updatedParams))

            logSection("Waiting for dapi to expose rollout sha for $versionName")
            waitUntil("dapi serves rollout sha for $versionName", timeoutSeconds = 60) {
                getDapiVersions().any { it["name"] == versionName && it["sha256"] == testappRolloutSha256 }
            }

            logSection("Waiting for versiond to route new requests to rollout binary")
            waitForVersionedProxyPrefix(versionName, newPrefix)

            logSection("Verifying old child is draining while the long request is still running")
            waitForDrainingChild(versionName, testappSha256)

            logSection("Verifying the long request completes on the old binary")
            val slowResponse = slowRequest.get(slowDuration.seconds + 45, TimeUnit.SECONDS)
            assertThat(slowResponse["prefix"])
                .withFailMessage("Long request should complete on old binary: $slowResponse")
                .isEqualTo(oldPrefix)

            logSection("Verifying new requests stay on the rollout binary")
            val newResponse = getVersiondProxy(versionName)
            assertThat(newResponse["prefix"]).isEqualTo(newPrefix)

            logSection("Waiting for old draining child to exit")
            waitForNoDraining(versionName)
        } finally {
            stopProbe.set(true)
            continuityProbe.get(5, TimeUnit.SECONDS)
        }

        assertThat(probeFailures)
            .withFailMessage("Versioned route returned errors during rolling update: $probeFailures")
            .isEmpty()
        assertThat(observedPrefixes)
            .withFailMessage("Continuity probe should see old and new prefixes")
            .contains(oldPrefix, newPrefix)
    }

    // ---------------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------------

    private fun InferenceParams.withApprovedVersions(
        versions: List<DevshardApprovedVersion>
    ): InferenceParams {
        val escrow = this.devshardEscrowParams ?: DevshardEscrowParams(
            minAmount = 5_000_000_000,
            maxAmount = 10_000_000_000,
            maxEscrowsPerEpoch = 100,
            groupSize = 16,
            tokenPrice = 1,
            maxNonce = 20_000,
        )
        return this.copy(
            devshardEscrowParams = escrow.copy(approvedVersions = versions)
        )
    }

    private fun waitForTestappArtifactSha256(artifactBase: String): String {
        var sha256: String? = null
        val deadline = System.currentTimeMillis() + 120_000
        while (sha256 == null && System.currentTimeMillis() < deadline) {
            try {
                val (_, _, result) = Fuel.get("$testappServerUrl/$artifactBase.zip.sha256")
                    .timeoutRead(5000)
                    .responseString()
                sha256 = result.get().trim()
            } catch (e: Exception) {
                Logger.debug("testapp-server artifact $artifactBase not ready: ${e.message}")
                Thread.sleep(2000)
            }
        }
        check(sha256 != null) {
            "testapp-server did not publish $artifactBase.zip.sha256 within 120s"
        }
        return sha256
    }

    @Suppress("UNCHECKED_CAST")
    private fun getDapiVersions(): List<Map<String, Any>> {
        return try {
            val (_, _, result) = Fuel.get("$dapiMlUrl/versions")
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

    @Suppress("UNCHECKED_CAST")
    private fun getNodeManagerTest(versionName: String): Map<String, String> {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/nodemanager-test")
            .timeoutRead(15_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/nodemanager-test returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, String>
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondProxy(versionName: String): Map<String, Any> {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/")
            .timeoutRead(10_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/ returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, Any>
    }

    private fun waitForVersionedProxy(versionName: String): Map<String, Any> {
        waitUntil("proxy routes $versionName", timeoutSeconds = 90) {
            try {
                getVersiondProxy(versionName)
                true
            } catch (e: Throwable) {
                Logger.debug("proxy route for $versionName not ready: ${e.message}")
                false
            }
        }
        return getVersiondProxy(versionName)
    }

    private fun waitForVersionedProxyPrefix(
        versionName: String,
        prefix: String,
        timeoutSeconds: Int = 90,
    ): Map<String, Any> {
        var matched: Map<String, Any>? = null
        waitUntil("proxy routes $versionName with prefix $prefix", timeoutSeconds = timeoutSeconds) {
            runCatching {
                val response = getVersiondProxy(versionName)
                if (response["prefix"] == prefix) {
                    matched = response
                    true
                } else {
                    false
                }
            }.getOrDefault(false)
        }
        return matched ?: getVersiondProxy(versionName)
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondProxySlow(versionName: String, duration: Duration): Map<String, Any> {
        val durationParam = "${duration.seconds}s"
        val (_, response, result) =
            Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/slow?duration=$durationParam")
                .timeout(5_000)
                .timeoutRead((duration.toMillis() + 30_000).toInt())
                .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/slow returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, Any>
    }

    @Suppress("UNCHECKED_CAST")
    private fun getDrainStatus(versionName: String): Map<String, Any> {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/$versionName/drain/status")
            .timeoutRead(10_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/$versionName/drain/status returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, Any>
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondHealth(): List<Map<String, Any>> {
        val (_, response, result) = Fuel.get("${genesis.api.getPublicUrl()}/devshard/healthz")
            .timeoutRead(10_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /devshard/healthz returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), List::class.java) as List<Map<String, Any>>
    }

    private fun waitForDrainingChild(versionName: String, sha256: String, timeoutSeconds: Int = 20) {
        waitUntil("old $versionName child is draining", timeoutSeconds = timeoutSeconds) {
            runCatching {
                getVersiondHealth().any {
                    it["name"] == versionName &&
                        it["sha256"] == sha256 &&
                        it["status"] == "draining"
                }
            }.getOrDefault(false)
        }
    }

    private fun waitForNoDraining(versionName: String, timeoutSeconds: Int = 60) {
        waitUntil("no draining child for $versionName", timeoutSeconds = timeoutSeconds) {
            runCatching {
                getVersiondHealth().none {
                    it["name"] == versionName && it["status"] == "draining"
                }
            }.getOrDefault(false)
        }
    }

    private fun statusNumber(status: Map<String, Any>, key: String): Long =
        when (val value = status[key]) {
            is Number -> value.toLong()
            is String -> value.toLong()
            else -> 0
        }

    private fun waitUntil(description: String, timeoutSeconds: Int, condition: () -> Boolean) {
        val deadline = System.currentTimeMillis() + timeoutSeconds * 1000L
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return
            Thread.sleep(2000)
        }
        error("Timed out waiting for: $description (${timeoutSeconds}s)")
    }

    companion object {
        /** Governance slot / --print-protocol-version for testapp.zip */
        const val TESTAPP_VERSION = "testapp"

        /** Governance slot / --print-protocol-version for testapp2.zip */
        const val TESTAPP2_VERSION = "testapp2"

        /** Artifact basename for same-slot rollout binary. Protocol remains TESTAPP_VERSION. */
        const val TESTAPP_ROLLOUT_ARTIFACT = "testapp-rollout"

        /** DEVSHARD_BINARY_LOG_VERSION / HTTP prefix printed by testapp-rollout.zip. */
        const val TESTAPP_ROLLOUT_PREFIX = "testapp-rollout"

        const val TESTAPP_SERVER_HOST_PORT = 7090
        const val DAPI_ML_HOST_PORT = 9001
    }
}
