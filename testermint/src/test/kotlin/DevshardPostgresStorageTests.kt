import com.productscience.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import java.time.Instant

/**
 * Verifies that when devshard sessions run in the local cluster, every row
 * lands in the shared testermint-postgres container -- not in the dapi
 * SQLite fallback. Tests connect to PG over the host-side mapped port via
 * PostgresClient and assert on the parent tables + per-epoch partitions.
 *
 * Postgres is unconditional infrastructure for this cluster (see
 * docker-compose.postgres.yml + DockerGroup.kt), so these tests run in the
 * default suite alongside DevshardTests.
 */
class DevshardPostgresStorageTests : TestermintTest() {
    private val devshardEscrowModel = defaultModel

    private val noRestrictionsConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(devshardNoRestrictionsSpec) ?: devshardNoRestrictionsSpec
    )

    @Test
    fun `devshard sessions, diffs, signatures land in postgres`() {
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshard-pg-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
        val escrow = genesis.node.queryDevshardEscrow(escrowId).escrow
            ?: error("escrow $escrowId not found on chain")
        val epochIndex = escrow.epochIndex.toLong()
        logSection("Escrow $escrowId is in epoch $epochIndex")

        logSection("Driving inferences through devshard proxy")
        val handle = genesis.startDevshardProxy(escrowId = escrowId, keyName = user.keyName)
        try {
            genesis.waitForDevshardProxyWarmup()
            for (i in 0 until 10) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "pg test prompt $i")
                assertThat(response).isNotEmpty()
            }
            // Settle so the session has its full lifecycle's worth of writes
            // committed to PG (sessions row, all diffs, all signatures).
            genesis.assertDevshardSettlement(handle, escrowId, user, escrowAmount, requireCompletedValidations = false)
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }

        logSection("Verifying devshard tables in Postgres")
        PostgresClient.connect().use { pg ->
            // Parent tables exist (created by Postgres backend's pgCreateParents).
            assertThat(pg.tableExists("devshard_sessions")).isTrue()
            assertThat(pg.tableExists("devshard_diffs")).isTrue()
            assertThat(pg.tableExists("devshard_signatures")).isTrue()

            // Per-epoch partitions for our escrow's epoch were created lazily
            // on first write. These are the things SQLite can't model.
            assertThat(pg.tableExists("devshard_sessions_epoch_$epochIndex")).isTrue()
            assertThat(pg.tableExists("devshard_diffs_epoch_$epochIndex")).isTrue()
            assertThat(pg.tableExists("devshard_signatures_epoch_$epochIndex")).isTrue()

            val whereEscrow = "epoch_id = $epochIndex AND escrow_id = '$escrowId'"

            // Exactly one session row.
            assertThat(pg.countRows("devshard_sessions", whereEscrow))
                .describedAs("devshard_sessions row count for escrow $escrowId")
                .isEqualTo(1)

            // latest_nonce -- read directly so we know how many diffs to expect.
            val latestNonce: Long = pg.firstColumn(
                "SELECT latest_nonce FROM devshard_sessions WHERE epoch_id = ? AND escrow_id = ?",
                epochIndex, escrowId.toString(),
            ) ?: error("no session row for escrow $escrowId")
            assertThat(latestNonce)
                .describedAs("latest_nonce for escrow $escrowId")
                .isGreaterThan(0)

            // Diffs row count must equal latest_nonce -- every host-applied
            // diff is persisted before the nonce is bumped.
            assertThat(pg.countRows("devshard_diffs", whereEscrow))
                .describedAs("devshard_diffs row count for escrow $escrowId")
                .isEqualTo(latestNonce)

            // Signatures: at least one row per nonce. We don't pin to an exact
            // count because slot count varies by group size and signature
            // delivery is async, but it must be > 0.
            assertThat(pg.countRows("devshard_signatures", whereEscrow))
                .describedAs("devshard_signatures row count for escrow $escrowId")
                .isGreaterThan(0)
        }

        logSection("Verifying SQLite path was NOT used")
        // SQLite session data lives in epoch_<N>.db files. _meta.db is the
        // routing index sidecar that NewSQLite always creates even when the
        // hybrid backend routes everything to Postgres, so we look only for
        // epoch_*.db files here.
        val out = genesis.api.executor.exec(
            listOf("sh", "-c", "ls /root/.dapi/data/devshard/epoch_*.db 2>/dev/null || true"),
            null,
        ).joinToString("\n").trim()
        assertThat(out)
            .describedAs("dapi data dir devshard/ must contain no epoch_*.db")
            .isEmpty()
    }

    // TODO: a third test that drives the same flow through the standalone
    // devshardd binary (versiond -> devshardd, mirroring DevshardStandaloneTests)
    // would close the loop on env propagation through versiond. It needs the
    // versiond compose extension + VERSIOND_BINARY_NAME=devshardd setup that
    // DevshardStandaloneTests already encapsulates -- factor that harness out
    // of DevshardStandaloneTests when adding here so we don't duplicate.

    @Test
    fun `devshard pruning drops only the target epoch partition`() {
        // Long enough run-time that we can advance past the retention window
        // (N=3 epochs in production). Keep the default 10-block epoch length
        // so the chain naturally ticks during the test.
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshard-pg-prune-user")
        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L

        logSection("Creating first escrow (will become the prune target)")
        val firstEscrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName, modelId = devshardEscrowModel)
        val firstEpoch = genesis.node.queryDevshardEscrow(firstEscrowId).escrow!!.epochIndex.toLong()

        run {
            val handle = genesis.startDevshardProxy(escrowId = firstEscrowId, keyName = user.keyName)
            try {
                genesis.waitForDevshardProxyWarmup()
                for (i in 0 until 5) {
                    genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "first $i")
                }
                genesis.assertDevshardSettlement(handle, firstEscrowId, user, escrowAmount, requireCompletedValidations = false)
            } finally {
                genesis.stopDevshardProxy(firstEscrowId)
            }
        }

        // Confirm partitions exist before we wait.
        PostgresClient.connect().use { pg ->
            assertThat(pg.tableExists("devshard_sessions_epoch_$firstEpoch")).isTrue()
            assertThat(pg.tableExists("devshard_diffs_epoch_$firstEpoch")).isTrue()
            assertThat(pg.tableExists("devshard_signatures_epoch_$firstEpoch")).isTrue()
        }

        // Advance the chain past firstEpoch + N (retain=3) so the pruner
        // ticks past it. Drive a fresh escrow + settlement on each iteration
        // so the cluster sees realistic activity. Each tick uses a NEW user
        // because assertDevshardSettlement asserts balance == fundAmount -
        // payout-of-this-settlement, which only holds for a single-shot user.
        val targetEpoch = firstEpoch + 4
        logSection("Advancing chain past epoch $targetEpoch so prune horizon clears $firstEpoch")
        var tick = 0
        while (genesis.getEpochData().latestEpoch.index < targetEpoch) {
            val tickUser = genesis.createFundedDevshardUser("devshard-pg-prune-tick-${tick++}")
            val newEscrowId = genesis.createDevshardEscrowForUser(escrowAmount, tickUser.keyName, modelId = devshardEscrowModel)
            val handle = genesis.startDevshardProxy(escrowId = newEscrowId, keyName = tickUser.keyName)
            try {
                genesis.waitForDevshardProxyWarmup()
                genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "tick")
                genesis.assertDevshardSettlement(handle, newEscrowId, tickUser, escrowAmount, requireCompletedValidations = false)
            } finally {
                genesis.stopDevshardProxy(newEscrowId)
            }
            genesis.waitForNextEpoch()
        }

        // ManagedStorage prunes on a 30s ticker. Wait up to 90s for the
        // first-epoch partitions to disappear from PG.
        logSection("Waiting for pruner to drop epoch $firstEpoch partitions")
        val deadline = Instant.now().plus(Duration.ofSeconds(90))
        var pruned = false
        while (Instant.now().isBefore(deadline) && !pruned) {
            PostgresClient.connect().use { pg ->
                val sessionsGone = !pg.tableExists("devshard_sessions_epoch_$firstEpoch")
                val diffsGone = !pg.tableExists("devshard_diffs_epoch_$firstEpoch")
                val sigsGone = !pg.tableExists("devshard_signatures_epoch_$firstEpoch")
                pruned = sessionsGone && diffsGone && sigsGone
            }
            if (!pruned) Thread.sleep(2_000)
        }
        assertThat(pruned)
            .describedAs("ManagedStorage must drop epoch $firstEpoch partitions within retention horizon")
            .isTrue()

        // Recent partitions must still be intact -- prune touches only the
        // target epoch.
        PostgresClient.connect().use { pg ->
            val recentEpoch = genesis.getEpochData().latestEpoch.index
            assertThat(pg.tableExists("devshard_sessions_epoch_$recentEpoch"))
                .describedAs("recent epoch $recentEpoch sessions partition must survive prune of $firstEpoch")
                .isTrue()
        }
    }
}
