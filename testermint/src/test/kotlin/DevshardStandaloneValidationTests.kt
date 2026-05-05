import com.github.dockerjava.core.DockerClientBuilder
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.time.Duration
import kotlin.test.assertNotNull

/**
 * Validation-specific coverage for standalone devshardd.
 *
 * Kept separate from DevshardStandaloneTests so CI can shard the expensive
 * validation scenarios independently from the broader devshard lifecycle suite.
 */
class DevshardStandaloneValidationTests : TestermintTest() {
    private val standaloneTestVersionName = "v0.2.11"
    private val overrideRoutePrefix = "/devshard/$standaloneTestVersionName"
    private val devshardEscrowModel = defaultModel
    private val devshardInferenceIdRegex = Regex(""""id"\s*:\s*"devshard-\d+-(\d+)"""")

    private val versiondComposeFilesByPairName = mapOf(
        GENESIS_KEY_NAME to listOf("docker-compose.versiond.yml", "docker-compose.devshard-postgres.yml"),
        "join1" to listOf("docker-compose.versiond.yml"),
        "join2" to listOf("docker-compose.versiond.yml"),
    )

    private val overrideVersiondEnv = mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        "VERSIOND_OVERRIDE_v0_2_11" to "/opt/overrides/devshardd",
        "VERSIOND_FORCE" to standaloneTestVersionName,
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

    private val overrideVersiondEnvFastRetry = overrideVersiondEnv +
            mapOf("DEVSHARD_VALIDATION_RETRY_INTERVAL" to "1s")

    private val overrideAlwaysValidateConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec, devshardAlwaysValidateSpec),
        env = overrideVersiondEnv,
    )

    private val overrideAlwaysValidateFastRetryConfig = versiondConfig(
        genesisSpec = mergedGenesisSpec(devshardNoRestrictionsSpec, devshardAlwaysValidateSpec),
        env = overrideVersiondEnvFastRetry,
    )

    /**
     * Steps:
     * 1. Boot a standalone devshardd cluster with validation forced for all eligible inferences.
     * 2. Configure every ML node to return the same valid response so re-execution should pass.
     * 3. Create a devshard escrow and send enough inferences to produce validator work.
     * 4. Finalize and settle through devshardctl, asserting settlement stats include completed validations.
     * 5. Query shared Postgres and assert validation leases reached submitted state with no pending/duplicate rows.
     */
    @Test
    fun `valid devshardd validation completes settlement stats and submitted leases`() {
        logSection("Step 1: boot devshardd cluster with always-validation config")
        val (cluster, genesis) = initCluster(config = overrideAlwaysValidateConfig, reboot = true)
        genesis.waitForNextEpoch()

        logSection("Step 2: configure all ML nodes to return matching validation responses")
        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(response = defaultInferenceResponseObject)
        }

        logSection("Step 3: create funded user and devshard escrow")
        val user = genesis.createFundedDevshardUser("devshardd-validation-valid-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
        logSection("Step 4: start devshard proxy for escrow $escrowId")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        val numInferences = 20L
        val settlement = try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Step 5: send $numInferences valid inferences through devshardd")
            for (i in 0 until numInferences) {
                genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "valid validation flow $i")
            }

            logSection("Step 6: finalize and settle; require completed validation stats")
            genesis.assertDevshardSettlement(
                handle,
                escrowId,
                user,
                escrowAmount,
                requireCompletedValidations = true,
                expectedVersion = standaloneTestVersionName,
            )
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }

        val completedValidations = settlement.parsed.hostStats.sumOf { it.completedValidations }
        assertThat(completedValidations)
            .withFailMessage("Expected settlement stats to include completed validations")
            .isGreaterThan(0)

        logSection("Step 7: assert submitted validation leases in Postgres")
        waitForSubmittedValidationLeases(escrowId)
        assertValidationLeaseState(escrowId)
    }

    /**
     * Steps:
     * 1. Boot a standalone devshardd cluster with validation forced for all eligible inferences.
     * 2. Configure most ML nodes to return a valid response and one executor candidate to return bad logits.
     * 3. Create a devshard escrow and send enough inferences to hit the invalid executor.
     * 4. Finalize and settle the escrow.
     * 5. Assert devshard state contains a challenged inference with invalid votes.
     * 6. Query shared Postgres and assert the invalid validation path still completed its lease.
     */
    @Test
    fun `invalid devshardd validation challenges inference and completes submitted lease`() {
        logSection("Step 1: boot devshardd cluster with always-validation config")
        val (cluster, genesis) = initCluster(config = overrideAlwaysValidateConfig, reboot = true)
        genesis.waitForNextEpoch()

        logSection("Step 2: configure one ML node to return invalid validation logits")
        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(
                response = defaultInferenceResponseObject,
                streamDelay = Duration.ofMillis(50),
            )
        }
        cluster.allPairs.last().mock?.stubDevshardResponseForAllSegments(
            response = defaultInferenceResponseObject.withMissingLogit(),
        )

        logSection("Step 3: create funded user and devshard escrow")
        val user = genesis.createFundedDevshardUser("devshardd-validation-invalid-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
        logSection("Step 4: start devshard proxy for escrow $escrowId")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        val numInferences = 20L
        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Step 5: send $numInferences inferences to exercise invalid validation")
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "invalid validation flow $i")
                assertThat(response).isNotEmpty()
            }

            logSection("Step 6: finalize and settle challenged escrow")
            genesis.waitForDevshardPreFinalize()
            val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
            assertThat(result.parsed.escrowId).isEqualTo("$escrowId")
            assertThat(result.parsed.version).isEqualTo(standaloneTestVersionName)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()

            val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = user.keyName)
            assertThat(settleResp.code).isEqualTo(0)

            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            assertThat(escrow.escrow!!.settled).isTrue()

            logSection("Step 7: assert invalid validation challenged an inference")
            val inference = assertNotNull(genesis.findChallengedDevshardInference(handle, numInferences))
            assertThat(inference.status).isEqualTo(DevshardInferenceStatus.CHALLENGED)
            assertThat(inference.votesInvalid).isNotZero()
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }

        logSection("Step 8: assert submitted validation leases in Postgres")
        waitForSubmittedValidationLeases(escrowId)
        assertValidationLeaseState(escrowId)
    }

    /**
     * Verifies the stale-lease retry loop by artificially aging acquired leases in Postgres.
     *
     * Steps:
     * 1. Boot devshardd with a 1s retry interval and always-validation config.
     * 2. Wait for an inference window with enough blocks before PoC, then send
     *    inferences and record the completed response IDs from this test run.
     * 3. Reset that specific lease to pending with a fresh claimed_at and assert
     *    the controlled setup before it is eligible for retry.
     * 4. Backdate claimed_at so the lease appears stale immediately
     *    (claimed_at = now() - 1 hour).
     * 5. Wait for the retry loop to fire and re-submit that lease.
     * 6. Assert the controlled lease is submitted again and is not left pending.
     */
    @Test
    fun `stale devshardd validation leases are retried by retry loop`() {
        logSection("Step 1: boot devshardd cluster with always-validation and 1s retry interval")
        val (cluster, genesis) = initCluster(config = overrideAlwaysValidateFastRetryConfig, reboot = true)
        genesis.waitForNextEpoch()

        logSection("Step 2: configure ML nodes to return matching responses")
        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(response = defaultInferenceResponseObject)
        }

        val user = genesis.createFundedDevshardUser("devshardd-retry-loop-user")
        genesis.waitForNextInferenceWindow(windowSizeInBlocks = 12)

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = overrideRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Step 3: send inferences and wait for one submitted lease from the completed responses")
            val completedInferenceIds = (0 until 20L).map { i ->
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "retry loop test $i")
                parseDevshardInferenceId(response) ?: (i + 1)
            }
            val targetInferenceIds = waitForLatestEpochSubmittedValidationLeaseIdsForInferences(
                escrowId = escrowId,
                inferenceIds = completedInferenceIds,
                expectedCount = 1,
            )
            val targetInferenceSql = targetInferenceIds.joinToString(",")

            logSection("Step 4: reset submitted lease(s) for inference(s) $targetInferenceIds back to pending")
            execPostgres(
                """
                UPDATE validation_leases
                SET status = 'pending', claimed_at = now()
                WHERE escrow_id = '$escrowId'
                  AND inference_id IN ($targetInferenceSql)
                  AND status = 'submitted'
                """.trimIndent()
            )
            val pendingAfterReset = queryValidationLeaseCount(
                escrowId,
                "inference_id IN ($targetInferenceSql) AND status = 'pending'",
            )
            assertThat(pendingAfterReset)
                .withFailMessage("Expected selected inference lease(s) $targetInferenceIds to be pending after reset")
                .isEqualTo(targetInferenceIds.size.toLong())

            logSection("Step 5: age selected lease(s) so the retry loop can reclaim them")
            execPostgres(
                """
                UPDATE validation_leases
                SET claimed_at = now() - interval '1 hour'
                WHERE escrow_id = '$escrowId'
                  AND inference_id IN ($targetInferenceSql)
                  AND status = 'pending'
                """.trimIndent()
            )

            logSection("Step 6: wait for retry loop to re-submit stale leases (interval=1s)")
            waitUntil("selected lease(s) re-submitted by retry loop", timeoutSeconds = 60) {
                queryValidationLeaseCount(
                    escrowId,
                    "inference_id IN ($targetInferenceSql) AND status = 'pending'",
                ) == 0L &&
                    queryValidationLeaseCount(
                        escrowId,
                        "inference_id IN ($targetInferenceSql) AND status = 'submitted'",
                    ) == targetInferenceIds.size.toLong()
            }

            val pendingAfterRetry = queryValidationLeaseCount(
                escrowId,
                "inference_id IN ($targetInferenceSql) AND status = 'pending'",
            )
            val submittedAfterRetry = queryValidationLeaseCount(
                escrowId,
                "inference_id IN ($targetInferenceSql) AND status = 'submitted'",
            )
            assertThat(pendingAfterRetry)
                .withFailMessage("Expected selected inference lease(s) $targetInferenceIds to leave pending after retry loop ran")
                .isZero()
            assertThat(submittedAfterRetry)
                .withFailMessage("Expected selected inference lease(s) $targetInferenceIds to be re-submitted after retry")
                .isEqualTo(targetInferenceIds.size.toLong())
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    private fun waitForSubmittedValidationLeases(escrowId: Long) {
        waitUntil("at least 1 submitted validation lease for escrow $escrowId", timeoutSeconds = 60) {
            queryValidationLeaseCount(escrowId, "status = 'submitted'") >= 1L
        }
    }

    private fun waitForLatestEpochSubmittedValidationLeaseIdsForInferences(
        escrowId: Long,
        inferenceIds: List<Long>,
        expectedCount: Int,
    ): List<Long> {
        var submittedLeaseIds = emptyList<Long>()
        waitUntil("submitted validation lease for escrow $escrowId in $inferenceIds", timeoutSeconds = 60) {
            submittedLeaseIds = queryLatestEpochSubmittedValidationLeaseIds(escrowId, inferenceIds)
                .take(expectedCount)
            submittedLeaseIds.size == expectedCount
        }
        return submittedLeaseIds
    }

    private fun assertValidationLeaseState(escrowId: Long) {
        val totalLeases = queryValidationLeaseCount(escrowId)
        val submittedLeases = queryValidationLeaseCount(escrowId, "status = 'submitted'")
        val pendingLeases = queryValidationLeaseCount(escrowId, "status = 'pending'")
        val skippedLeases = queryValidationLeaseCount(escrowId, "status = 'skipped'")
        val duplicateLeaseKeys = queryPostgresLong(
            """
            SELECT COUNT(*) FROM (
              SELECT escrow_id, inference_id
              FROM validation_leases
              WHERE escrow_id = '$escrowId'
              GROUP BY escrow_id, inference_id
              HAVING COUNT(*) > 1
            ) duplicates
            """.trimIndent()
        )

        Logger.info(
            "validation leases: total=$totalLeases submitted=$submittedLeases " +
                    "pending=$pendingLeases skipped=$skippedLeases duplicateKeys=$duplicateLeaseKeys " +
                    "for escrow $escrowId"
        )

        assertThat(submittedLeases)
            .withFailMessage("Expected at least 1 submitted validation lease, got 0")
            .isGreaterThanOrEqualTo(1L)
        assertThat(totalLeases)
            .withFailMessage("Expected submitted leases to be a subset of all leases")
            .isGreaterThanOrEqualTo(submittedLeases)
        assertThat(pendingLeases)
            .withFailMessage("Expected no pending leases left after validation submission")
            .isZero()
        assertThat(skippedLeases)
            .withFailMessage("Expected no skipped leases — epoch should not have advanced mid-test")
            .isZero()
        assertThat(duplicateLeaseKeys)
            .withFailMessage("Expected lease primary key to deduplicate escrow/inference rows")
            .isZero()
    }

    private fun queryValidationLeaseCount(escrowId: Long, predicate: String? = null): Long {
        val where = listOfNotNull("escrow_id = '$escrowId'", predicate).joinToString(" AND ")
        return queryPostgresLong("SELECT COUNT(*) FROM validation_leases WHERE $where")
    }

    private fun queryLatestEpochSubmittedValidationLeaseIds(escrowId: Long, inferenceIds: List<Long>): List<Long> {
        if (inferenceIds.isEmpty()) return emptyList()

        val inferenceIdsSql = inferenceIds.joinToString(",")
        return queryPostgresLongRows(
            """
            SELECT inference_id
            FROM validation_leases
            WHERE escrow_id = '$escrowId'
              AND inference_id IN ($inferenceIdsSql)
              AND status = 'submitted'
              AND epoch_id = (
                SELECT MAX(epoch_id)
                FROM validation_leases
                WHERE escrow_id = '$escrowId'
                  AND inference_id IN ($inferenceIdsSql)
                  AND status = 'submitted'
              )
            ORDER BY inference_id
            """.trimIndent()
        )
    }

    private fun parseDevshardInferenceId(response: String): Long? =
        devshardInferenceIdRegex.find(response)
            ?.groupValues
            ?.get(1)
            ?.toLongOrNull()

    private fun queryPostgresLong(sql: String): Long =
        postgresSingleLongFromOutput(queryPostgres(sql), sql)

    private fun queryPostgresLongRows(sql: String): List<Long> =
        postgresLongRowsFromOutput(queryPostgres(sql), sql)

    private fun postgresContainerId(): String =
        DockerClientBuilder.getInstance().build().use { dockerClient ->
            dockerClient.listContainersCmd().exec()
                .find { c -> c.names.any { it == "/devshard-postgres" } }
                ?.id ?: error("devshard-postgres container not found")
        }

    private fun queryPostgres(sql: String): List<String> =
        DockerExecutor(postgresContainerId(), inferenceConfig).exec(
            listOf("psql", "-v", "ON_ERROR_STOP=1", "-U", "devshardd", "-d", "devshardd", "-t", "-A", "-c", sql),
            null,
        ).also { requirePostgresSuccess(it, sql) }

    private fun execPostgres(sql: String) {
        val output = DockerExecutor(postgresContainerId(), inferenceConfig).exec(
            listOf("psql", "-v", "ON_ERROR_STOP=1", "-U", "devshardd", "-d", "devshardd", "-c", sql),
            null,
        )
        requirePostgresSuccess(output, sql)
    }

    private fun versiondConfig(
        genesisSpec: Spec<AppState>,
        env: Map<String, String>,
    ): ApplicationConfig = inferenceConfig.copy(
        genesisSpec = genesisSpec,
        devshardProxyImageName = "devshardd:latest",
        additionalDockerFilesByKeyName = versiondComposeFilesByPairName,
        additionalEnvVars = env,
    )

    private fun mergedGenesisSpec(vararg specs: Spec<AppState>): Spec<AppState> {
        val base = inferenceConfig.genesisSpec ?: spec<AppState> {}
        return specs.fold(base) { current, extra -> current.merge(extra) }
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

internal fun requirePostgresSuccess(output: List<String>, sql: String) {
    val fullOutput = output.joinToString("")
    val hasError = fullOutput
        .lineSequence()
        .map { it.trimStart() }
        .any { it.startsWith("ERROR:") || it.startsWith("psql:") && it.contains("error:", ignoreCase = true) }
    check(!hasError) {
        "Postgres command failed for SQL:\n$sql\nOutput:\n$fullOutput"
    }
}

internal fun postgresLongRowsFromOutput(output: List<String>, sql: String): List<Long> {
    requirePostgresSuccess(output, sql)
    return output
        .flatMap { it.lineSequence().toList() }
        .mapNotNull { it.trim().toLongOrNull() }
}

internal fun postgresSingleLongFromOutput(output: List<String>, sql: String): Long =
    postgresLongRowsFromOutput(output, sql).firstOrNull()
        ?: error("Postgres query returned no numeric rows for SQL:\n$sql\nOutput:\n${output.joinToString("")}")
