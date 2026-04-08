import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.*
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

/**
 * Full-circle E2E tests for versiond:
 *   1. Local chain + versiond container
 *   2. Verify empty approved_versions on startup
 *   3. Governance proposal adds a subnet binary version
 *   4. versiond downloads the binary and proxies traffic
 *   5. Second proposal adds another version, both route correctly
 *
 * Requires docker-compose.versiond.yml (adds versiond + testapp-server services).
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
@Timeout(value = 10, unit = TimeUnit.MINUTES)
class VersiondTests : TestermintTest() {

    private val versiondUrl = "http://localhost:$VERSIOND_HOST_PORT"
    private val testappServerUrl = "http://localhost:$TESTAPP_SERVER_HOST_PORT"
    private val dapiMlUrl = "http://localhost:$DAPI_ML_HOST_PORT"
    private val testappBinaryDockerUrl = "http://${GENESIS_KEY_NAME}-testapp-server:8080/testapp.zip"

    private lateinit var cluster: LocalCluster
    private lateinit var genesis: LocalInferencePair
    private lateinit var testappSha256: String

    @BeforeAll
    fun setup() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName = mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose.versiond.yml")
            )
        )
        val (c, g) = initCluster(config = config, reboot = true)
        cluster = c
        genesis = g

        logSection("Waiting for testapp-server readiness")
        testappSha256 = waitForTestappServer()
        Logger.info("testapp zip sha256: $testappSha256")

        logSection("Waiting for versiond readiness")
        waitForVersiondHealthz()
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
        val approvedVersions = params.subnetEscrowParams?.approvedVersions ?: emptyList()
        assertThat(approvedVersions)
            .withFailMessage("Expected no approved versions in initial chain params")
            .isEmpty()

        logSection("Verifying dapi serves empty versions list")
        val dapiVersions = getDapiVersions()
        assertThat(dapiVersions)
            .withFailMessage("Expected dapi /versions to return empty list")
            .isEmpty()

        logSection("Verifying versiond has no running versions")
        val health = getVersiondHealth()
        assertThat(health)
            .withFailMessage("Expected versiond /healthz to report no running versions")
            .isEmpty()
    }

    @Test
    @Order(2)
    fun `governance proposal adds first version and versiond downloads it`() {
        val versionName = "v0.2.11"

        logSection("Submitting governance proposal to add $versionName")
        val params = genesis.getParams()
        val updatedParams = params.withApprovedVersions(
            listOf(
                SubnetApprovedVersion(
                    name = versionName,
                    binary = testappBinaryDockerUrl,
                    sha256 = testappSha256,
                )
            )
        )
        genesis.runProposal(cluster, UpdateParams(params = updatedParams))

        logSection("Verifying chain params updated")
        val newParams = genesis.getParams()
        val versions = newParams.subnetEscrowParams?.approvedVersions ?: emptyList()
        assertThat(versions).hasSize(1)
        assertThat(versions[0].name).isEqualTo(versionName)
        assertThat(versions[0].sha256).isEqualTo(testappSha256)

        logSection("Waiting for dapi to serve the new version")
        waitUntil("dapi serves $versionName", timeoutSeconds = 30) {
            getDapiVersions().any { it["name"] == versionName }
        }

        logSection("Waiting for versiond to download and start $versionName")
        waitUntil("versiond starts $versionName", timeoutSeconds = 90) {
            getVersiondHealth().any {
                it["name"] == versionName && it["status"] == "running"
            }
        }

        logSection("Verifying proxy routing through versiond")
        val response = getVersiondProxy(versionName)
        assertThat(response["prefix"])
            .withFailMessage("Expected testapp to report prefix=$versionName, got ${response["prefix"]}")
            .isEqualTo(versionName)
        logHighlight("$versionName routed successfully through versiond")
    }

    @Test
    @Order(3)
    fun `governance proposal adds second version and both route`() {
        val v1 = "v0.2.11"
        val v2 = "v0.2.12"

        logSection("Submitting governance proposal to add $v2 (keeping $v1)")
        val params = genesis.getParams()
        val updatedParams = params.withApprovedVersions(
            listOf(
                SubnetApprovedVersion(
                    name = v1,
                    binary = testappBinaryDockerUrl,
                    sha256 = testappSha256,
                ),
                SubnetApprovedVersion(
                    name = v2,
                    binary = testappBinaryDockerUrl,
                    sha256 = testappSha256,
                ),
            )
        )
        genesis.runProposal(cluster, UpdateParams(params = updatedParams))

        logSection("Verifying chain params have both versions")
        val newParams = genesis.getParams()
        val versions = newParams.subnetEscrowParams?.approvedVersions ?: emptyList()
        assertThat(versions).hasSize(2)
        assertThat(versions.map { it.name }).containsExactlyInAnyOrder(v1, v2)

        logSection("Waiting for versiond to start $v2")
        waitUntil("versiond starts $v2", timeoutSeconds = 90) {
            val health = getVersiondHealth()
            health.any { it["name"] == v2 && it["status"] == "running" }
        }

        logSection("Verifying $v1 still routes")
        val resp1 = getVersiondProxy(v1)
        assertThat(resp1["prefix"]).isEqualTo(v1)

        logSection("Verifying $v2 routes")
        val resp2 = getVersiondProxy(v2)
        assertThat(resp2["prefix"]).isEqualTo(v2)

        logHighlight("Both $v1 and $v2 route correctly through versiond")
    }

    // ---------------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------------

    private fun InferenceParams.withApprovedVersions(
        versions: List<SubnetApprovedVersion>
    ): InferenceParams {
        val escrow = this.subnetEscrowParams ?: SubnetEscrowParams(
            minAmount = 5_000_000_000,
            maxAmount = 10_000_000_000,
            maxEscrowsPerEpoch = 100,
            groupSize = 16,
            tokenPrice = 1,
        )
        return this.copy(
            subnetEscrowParams = escrow.copy(approvedVersions = versions)
        )
    }

    private fun waitForTestappServer(): String {
        var sha256: String? = null
        val deadline = System.currentTimeMillis() + 120_000
        while (sha256 == null && System.currentTimeMillis() < deadline) {
            try {
                val (_, _, result) = Fuel.get("$testappServerUrl/testapp.zip.sha256")
                    .timeoutRead(5000)
                    .responseString()
                sha256 = result.get().trim()
            } catch (e: Exception) {
                Logger.debug("testapp-server not ready: ${e.message}")
                Thread.sleep(2000)
            }
        }
        check(sha256 != null) { "testapp-server did not become ready within 120s" }
        return sha256
    }

    private fun waitForVersiondHealthz() {
        val deadline = System.currentTimeMillis() + 120_000
        while (System.currentTimeMillis() < deadline) {
            try {
                val (_, response, _) = Fuel.get("$versiondUrl/healthz")
                    .timeoutRead(5000)
                    .responseString()
                if (response.statusCode == 200) return
            } catch (e: Exception) {
                Logger.debug("versiond not ready: ${e.message}")
            }
            Thread.sleep(2000)
        }
        error("versiond /healthz did not become ready within 120s")
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
    private fun getVersiondHealth(): List<Map<String, Any>> {
        return try {
            val (_, _, result) = Fuel.get("$versiondUrl/healthz")
                .timeoutRead(5000)
                .responseString()
            cosmosJson.fromJson(result.get(), List::class.java) as? List<Map<String, Any>> ?: emptyList()
        } catch (e: Exception) {
            Logger.warn("Failed to query versiond /healthz: ${e.message}")
            emptyList()
        }
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondProxy(versionName: String): Map<String, Any> {
        val (_, response, result) = Fuel.get("$versiondUrl/$versionName/")
            .timeoutRead(10_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /$versionName/ returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, Any>
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
        const val VERSIOND_HOST_PORT = 7080
        const val TESTAPP_SERVER_HOST_PORT = 7090
        const val DAPI_ML_HOST_PORT = 9001
    }
}
