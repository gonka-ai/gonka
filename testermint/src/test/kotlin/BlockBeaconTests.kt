import com.productscience.ApplicationConfig
import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import org.tinylog.kotlin.Logger
import com.productscience.createSpec
import com.productscience.inferenceConfig
import com.productscience.logSection
import com.productscience.normalizeValidatorAddress
import com.productscience.setupLocalCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Assumptions.assumeTrue
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

/**
 * Block BLS beacon (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md):
 *  - Step 1: CometBFT vote extensions enabled with empty extensions.
 *  - Step 2: the share bridge. After DKG the dAPI exports each validator's BLS slot shares to
 *    /root/.inference/bls-beacon/epoch-<N>.json and the node verifies them against committed
 *    state (log `beacon shares loaded ... valid=true`).
 *  - Step 3: real extensions. Once a DKG completes and becomes the beacon key (rule R9), every
 *    validator signs PendingBeacon(h) with its slots in ExtendVote (metrics on :26660).
 *
 * Env:
 *  - TESTERMINT_TMKMS_IMAGE: vote-extension-capable tmkms image (tmkms 0.14.0 with
 *    sign_extensions = true; 0.15.0 drops the extension signature, tmkms#1174), e.g. built with
 *    `make -C tmkms build-with-keygen VERSION=ve-local` →
 *    ghcr.io/product-science/tmkms-softsign-with-keygen:ve-local. Tmkms cases are
 *    skipped when unset.
 *  - TESTERMINT_TMKMS_LEGACY_IMAGE: legacy tmkms image for the negative case
 *    (default: the production image ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16, tmkms 0.12.0).
 */
@Timeout(value = 30, unit = TimeUnit.MINUTES)
class BlockBeaconTests : TestermintTest() {

    private val enableHeight = 2L
    private val SNAPSHOT_INTERVAL = 100L // keep in sync with DockerGroup getCommonEnvMap
    private val veConfig: ApplicationConfig = inferenceConfig.copy(voteExtensionsEnableHeight = enableHeight)

    // Share-bridge cases need the signing epoch to stay put while a file is deleted and the
    // dAPI restarts; the default 15-block epoch rotates in ~80 s, about one dAPI restart.
    private val shareConfig: ApplicationConfig = veConfig.copy(genesisSpec = createSpec(epochLength = 40L))

    @Test
    @Tag("block-beacon")
    fun `ve enabled empty extensions`() {
        logSection("Cluster with vote extensions enabled at height $enableHeight")
        val cluster = setupLocalCluster(2, veConfig, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }

        assertGenesisEnableHeight(cluster)
        val tip = waitPastEnableHeight(cluster.genesis, blocksAfter = 20)
        assertAllValidatorsSigning(cluster, tip, window = 20)
        assertNoConsensusFailure(cluster.allPairs)
    }

    @Test
    @Tag("block-beacon")
    @Tag("tmkms")
    fun `ve enabled with tmkms signing extensions on join nodes`() {
        val image = System.getenv("TESTERMINT_TMKMS_IMAGE")
        assumeTrue(!image.isNullOrBlank(), "TESTERMINT_TMKMS_IMAGE not set (vote-extension-capable tmkms build)")

        logSection("Join nodes sign through tmkms ($image)")
        val cluster = setupLocalCluster(2, tmkmsConfig(image!!, listOf("join1", "join2")), reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        assertGenesisEnableHeight(cluster)

        cluster.joinPairs.forEach { waitUntilCometValidator(it) }
        // Measure only once both tmkms-signed join validators have been active for a while.
        val joinedBy = cluster.genesis.getCurrentBlockHeight()
        val tip = cluster.genesis.node.waitForMinimumBlock(joinedBy + 22, "tmkms join validators signing with extensions")
        assertAllValidatorsSigning(cluster, tip, window = 20)
        val active = cluster.genesis.node.getCometValidatorsAt(tip - 1).validators.map { toHexAddress(it.address) }.toSet()
        cluster.joinPairs.forEach { pair ->
            assertThat(active).describedAs("${pair.name} is an active validator").contains(toHexAddress(pair.node.getStatus().validatorInfo.address))
        }
        assertNoConsensusFailure(cluster.allPairs)

        // tmkms init.sh prints the (migrated) tmkms.toml before starting, then
        // tmkms logs every extension signature.
        cluster.joinPairs.forEach { pair ->
            val logs = pair.readSiblingLogs("tmkms", tail = 5000)
            assertThat(logs).describedAs("${pair.name} tmkms config").contains("sign_extensions = true")
            assertThat(logs).describedAs("${pair.name} tmkms config").contains("protocol_version = \"v0.34\"")
            assertThat(logs).describedAs("${pair.name} tmkms signed extensions").contains("signed vote extension")
        }
    }

    @Test
    @Tag("block-beacon")
    @Tag("tmkms")
    fun `legacy tmkms cannot sign extensions and the node halts`() {
        val image = System.getenv("TESTERMINT_TMKMS_LEGACY_IMAGE")?.takeIf { it.isNotBlank() }
            ?: "ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16"

        logSection("join1 signs through legacy tmkms ($image); expect consensus failure once it precommits")
        val cluster = setupLocalCluster(2, tmkmsConfig(image, listOf("join1")), reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        val legacy = cluster.joinPairs.first { it.name.trimStart('/') == "join1" }

        // join1 only signs precommits once it is in the active validator set. The
        // chain may halt the moment it joins (equal-power validators: losing join1
        // leaves exactly 2/3), so don't use a wait that requires new blocks.
        val legacyAddress = toHexAddress(legacy.node.getStatus().validatorInfo.address)
        val failureMarkers = listOf("non-recoverable error when signing vote", "CONSENSUS FAILURE")
        val deadline = System.nanoTime() + TimeUnit.MINUTES.toNanos(12)
        var nodeLogs = ""
        var joinedAt: Long? = null
        var lastHeight = -1L
        var stalledSince: Long? = null
        while (System.nanoTime() < deadline) {
            nodeLogs = legacy.readSiblingLogs("node", tail = 5000)
            if (failureMarkers.any { nodeLogs.contains(it) }) break

            val height = runCatching { cluster.genesis.node.getStatus().syncInfo.latestBlockHeight }.getOrDefault(lastHeight)
            if (joinedAt == null) {
                val isValidator = runCatching {
                    cluster.genesis.node.getCometValidators().validators.any { toHexAddress(it.address) == legacyAddress }
                }.getOrDefault(false)
                if (isValidator) {
                    joinedAt = height
                    logSection("join1 joined the validator set at height $height")
                }
            }
            if (height == lastHeight) {
                stalledSince = stalledSince ?: System.nanoTime()
            } else {
                stalledSince = null
                lastHeight = height
            }
            // A halt right after join1 joined, sustained for 2 minutes, is the
            // expected outcome even if the panic text is not (yet) in the log tail.
            if (joinedAt != null && stalledSince != null &&
                System.nanoTime() - stalledSince > TimeUnit.MINUTES.toNanos(2)) break
            Thread.sleep(5_000)
        }

        // Keep the evidence: the next test's cluster reboot wipes container logs.
        val tmkmsLogs = runCatching { legacy.readSiblingLogs("tmkms", tail = 5000) }.getOrDefault("")
        val evidenceDir = java.nio.file.Path.of("logs")
        java.nio.file.Files.createDirectories(evidenceDir)
        java.nio.file.Files.writeString(evidenceDir.resolve("legacy-tmkms-join1-node.log"), nodeLogs)
        java.nio.file.Files.writeString(evidenceDir.resolve("legacy-tmkms-join1-tmkms.log"), tmkmsLogs)

        assertThat(tmkmsLogs).describedAs("join1 must actually run the legacy tmkms").contains("tmkms 0.12")
        assertThat(joinedAt).describedAs("join1 joined the active validator set (precondition)").isNotNull()
        val panicked = failureMarkers.any { nodeLogs.contains(it) }
        val chainHalted = stalledSince != null && lastHeight <= joinedAt!! + 1
        assertThat(panicked || chainHalted)
            .describedAs(
                "legacy tmkms must break join1 once it precommits with vote extensions on " +
                    "(panic logged: $panicked, chain halted at $lastHeight after join at $joinedAt)"
            )
            .isTrue()
    }

    /**
     * Step 0 rehearsal: an operator swaps tmkms 0.12 → new image on a running
     * validator, keeping the `.tmkms` volume (key, config, double-sign state).
     * Vote extensions stay off (enable height far in the future), as on mainnet
     * before the v0.2.18 proposal. Expect: in-place config migration, no
     * double-sign refusal, signing resumes, state height keeps increasing, and
     * tmkms already logs `signed vote extension` (the operator readiness signal).
     */
    @Test
    @Tag("block-beacon")
    @Tag("tmkms")
    fun `tmkms image swap keeps signing and state`() {
        val newImage = System.getenv("TESTERMINT_TMKMS_IMAGE")
        assumeTrue(!newImage.isNullOrBlank(), "TESTERMINT_TMKMS_IMAGE not set (vote-extension-capable tmkms build)")
        val legacyImage = System.getenv("TESTERMINT_TMKMS_LEGACY_IMAGE")?.takeIf { it.isNotBlank() }
            ?: "ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16"

        // Far-future enable height: VE never activates in this test, and the
        // cluster won't match (and be reused by) suites that run with VE off.
        val config = inferenceConfig.copy(
            voteExtensionsEnableHeight = 1_000_000L,
            additionalDockerFilesByKeyName = mapOf("join1" to listOf("docker-compose.tmkms.yml")),
            additionalEnvVars = inferenceConfig.additionalEnvVars + ("TMKMS_IMAGE" to legacyImage),
        )
        val cluster = setupLocalCluster(2, config, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        val join1 = cluster.joinPairs.first { it.name.trimStart('/') == "join1" }
        val tmkms = "${join1.name.trimStart('/')}-tmkms"

        waitUntilCometValidator(join1)
        val join1Address = toHexAddress(join1.node.getStatus().validatorInfo.address)
        val beforeTip = cluster.genesis.node.waitForMinimumBlock(cluster.genesis.getCurrentBlockHeight() + 10, "legacy tmkms signing")
        assertParticipation(cluster, join1Address, beforeTip - 9, beforeTip, "legacy tmkms")
        val stateBefore = tmkmsStateHeight(tmkms)

        logSection("Swapping $tmkms: $legacyImage → $newImage (same volume)")
        val volume = docker("inspect", "-f", "{{range .Mounts}}{{if eq .Destination \"/root/.tmkms\"}}{{.Source}}{{end}}{{end}}", tmkms).trim()
        assertThat(volume).describedAs("tmkms volume source").isNotBlank()
        val network = docker("inspect", "-f", "{{range \$k, \$v := .NetworkSettings.Networks}}{{\$k}} {{end}}", tmkms).trim().split(" ").first()
        try {
            docker("rm", "-f", tmkms)
            docker(
                "run", "-d", "--name", tmkms, "--restart", "unless-stopped", "--network", network,
                "-e", "VALIDATOR_LISTEN_ADDRESS=tcp://${join1.name.trimStart('/')}-node:26658",
                "-v", "$volume:/root/.tmkms", newImage!!,
            )

            val swapHeight = cluster.genesis.getCurrentBlockHeight()
            val afterTip = cluster.genesis.node.waitForMinimumBlock(swapHeight + 15, "signing after tmkms swap")
            assertParticipation(cluster, join1Address, swapHeight + 5, afterTip, "new tmkms")

            val logs = join1.readSiblingLogs("tmkms", tail = 5000)
            assertThat(logs).contains("Migrated /root/.tmkms/tmkms.toml")
            assertThat(logs).contains("tmkms version: 0.14.0")
            assertThat(logs).describedAs("readiness signal before VE is enabled").contains("signed vote extension")
            assertThat(logs.lowercase()).describedAs("double-sign protection must accept the old state").doesNotContain("double sign")
            assertThat(tmkmsStateHeight(tmkms)).isGreaterThan(stateBefore)
        } finally {
            // The manually started container is outside the compose project;
            // remove it so the next cluster init can recreate join1-tmkms.
            runCatching { docker("rm", "-f", tmkms) }
        }
    }

    @Test
    @Tag("block-beacon")
    fun `snapshot join after enable height catches up`() {
        val cluster = setupLocalCluster(2, veConfig, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        // State sync needs a snapshot past E. Nodes snapshot every SNAPSHOT_INTERVAL=100
        // blocks (DockerGroup common env), so wait until at least one exists.
        cluster.genesis.node.waitForMinimumBlock(SNAPSHOT_INTERVAL + 10, "first state-sync snapshot after vote-extension enable height")

        logSection("Adding a join node via state sync")
        val grown = cluster.withAdditionalJoin(1)
        val joiner = grown.joinPairs.last()
        joiner.waitForFirstBlock()
        val target = cluster.genesis.getCurrentBlockHeight() + 10
        joiner.node.waitForMinimumBlock(target, "snapshot joiner catches up past vote-extension enable height")
        assertThat(joiner.node.getStatus().syncInfo.catchingUp).isFalse()
        assertNoConsensusFailure(listOf(joiner))
    }

    @Test
    @Tag("block-beacon")
    @Tag("share-bridge")
    fun `share file exported`() {
        val cluster = setupLocalCluster(2, shareConfig, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        val epochId = waitForSignedShareEpoch(cluster)

        logSection("Share files for epoch $epochId on every validator")
        val epochData = cluster.genesis.node.queryBLSEpochData(epochId).epochData
        val owners = cluster.allPairs.map { pair ->
            val file = assertShareFile(pair, epochId)
            val participant = epochData.participants.firstOrNull { it.address == file.participant }
            assertThat(participant).describedAs("${pair.name} share file participant ${file.participant} in epoch $epochId").isNotNull
            assertThat(file.slotStart to file.slotEnd)
                .describedAs("${pair.name} share file slot range")
                .isEqualTo(participant!!.slotStartIndex to participant.slotEndIndex)
            file.participant
        }
        assertThat(owners.toSet()).describedAs("one participant per validator").hasSize(cluster.allPairs.size)

        logSection("Shares live in a memory-only volume shared by node and api only (M1)")
        cluster.allPairs.forEach { pair ->
            assertShareDirIsTmpfs(nodeContainer(pair))
            assertShareDirIsTmpfs("${containerBase(pair)}-api")
        }

        logSection("Nodes verified the shares against committed state and their own validator (M2)")
        cluster.allPairs.forEach { pair ->
            waitForSharesLoaded(pair, epochId, minCount = 1)
            assertThat(shareLogLines(pair, epochId, valid = false)).describedAs("${pair.name} rejected shares").isEmpty()
            // The identity the provider checks against is the one CometBFT signs with.
            val own = normalizeValidatorAddress(pair.node.getStatus().validatorInfo.address)
            val logged = nodeLogLines(pair).firstNotNullOfOrNull { Regex("beacon validator identity.*cons_address=([0-9A-F]{40})").find(it)?.groupValues?.get(1) }
            assertThat(logged).describedAs("${pair.name} beacon validator identity").isEqualTo(own)
        }

        logSection("A share file copied from another validator is never used (M2)")
        val donor = cluster.genesis
        val victim = cluster.joinPairs.last()
        val loadedBefore = shareLogLines(victim, epochId, valid = true).size
        // Stop the victim's dAPI so its reconcile loop cannot repair the file before
        // the node judges it (the tmpfs stays mounted by the node).
        docker("stop", "${containerBase(victim)}-api")
        copyShareFile(from = donor, to = victim, epochId = epochId)
        val foreign = waitForShareRejection(victim, epochId)
        assertThat(foreign).describedAs("${victim.name} rejection reason").contains("this node signs as")

        logSection("The dAPI replaces the foreign file with its own shares (review L2)")
        docker("start", "${containerBase(victim)}-api")
        waitForSharesLoaded(victim, epochId, minCount = loadedBefore + 1)
        assertThat(apiLogLines(victim).any { it.contains("content differs") && it.contains("epoch=$epochId ") })
            .describedAs("${victim.name} dAPI logged the rewrite").isTrue()
        assertNoConsensusFailure(cluster.allPairs)
    }

    @Test
    @Tag("block-beacon")
    @Tag("share-bridge")
    fun `share file re-exported after dapi restart`() {
        val cluster = setupLocalCluster(2, shareConfig, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }
        val epochId = waitForSignedShareEpoch(cluster)
        val pair = cluster.joinPairs.first()
        waitForSharesLoaded(pair, epochId, minCount = 1)
        val before = assertShareFile(pair, epochId)
        val loadedBefore = shareLogLines(pair, epochId, valid = true).size

        logSection("Delete ${pair.name}'s share file and restart its dAPI")
        docker("exec", nodeContainer(pair), "rm", "-f", sharePath(epochId))
        docker("restart", "${containerBase(pair)}-api")

        // Startup reconcile recovers the epoch from chain and writes the file again.
        pair.node.waitForNextBlock(1)
        val deadline = System.currentTimeMillis() + 180_000
        while (!shareFileExists(pair, epochId)) {
            check(System.currentTimeMillis() < deadline) { "share file for epoch $epochId not re-exported on ${pair.name} within 3 minutes" }
            check(signingEpoch(cluster) == epochId) { "signing epoch moved past $epochId before the re-export; lengthen the epoch" }
            Thread.sleep(2_000)
        }
        val after = assertShareFile(pair, epochId)
        assertThat(after.sha256).describedAs("re-exported shares are identical").isEqualTo(before.sha256)

        logSection("Node reloads and re-verifies the file")
        waitForSharesLoaded(pair, epochId, minCount = loadedBefore + 1)
        assertThat(nodeLogLines(pair).any { it.contains("beacon shares unloaded") && it.contains("epoch=$epochId ") }).isTrue()
        assertNoConsensusFailure(cluster.allPairs)
    }

    @Test
    @Tag("block-beacon")
    @Tag("real-extensions")
    fun `real extensions after dkg`() {
        val cluster = setupLocalCluster(2, shareConfig, reboot = true)
        cluster.allPairs.forEach { it.waitForFirstBlock() }

        logSection("Before a beacon key exists: empty extensions, nothing rejected")
        cluster.genesis.node.waitForMinimumBlock(enableHeight + 5, "a few blocks with vote extensions on")
        val early = nodeMetrics(cluster.genesis)
        assertThat(metric(early, "beacon_extend_vote_empty_total", "reason" to "no_pending") +
            metric(early, "beacon_extend_vote_empty_total", "reason" to "no_shares"))
            .describedAs("genesis empty extensions before the key").isGreaterThan(0.0)

        logSection("Wait for a beacon key held by all validators and their shares")
        val keyEpoch = waitForBeaconKeyOverAllValidators(cluster)
        val ranges = cluster.allPairs.associateWith { pair ->
            val f = assertShareFile(pair, keyEpoch)
            f.slotEnd - f.slotStart + 1
        }
        cluster.allPairs.forEach { waitForSharesLoaded(it, keyEpoch, minCount = 1) }
        val keyedFrom = cluster.genesis.getCurrentBlockHeight()
        cluster.genesis.node.waitForMinimumBlock(keyedFrom + 20, "20 blocks of partials")

        logSection("Every validator signs its full slot range; nobody rejects")
        val pending = queryPendingBeacon(cluster.genesis, cluster.genesis.getCurrentBlockHeight())
        assertThat(pending?.keyEpoch).describedAs("pending beacon key epoch").isEqualTo(keyEpoch)
        cluster.allPairs.forEach { pair ->
            val m = nodeMetrics(pair)
            assertThat(metric(m, "beacon_extend_vote_slots")).describedAs("${pair.name} slots signed")
                .isEqualTo(ranges.getValue(pair).toDouble())
            assertThat(metric(m, "beacon_extend_vote_signed_total")).describedAs("${pair.name} extensions signed")
                .isGreaterThanOrEqualTo(15.0)
            assertThat(rejects(m)).describedAs("${pair.name} rejected extensions").isEqualTo(0.0)
            assertThat(metric(m, "beacon_verify_vote_ext_total", "result" to "accept", "reason" to "within_cap"))
                .describedAs("${pair.name} accepted non-empty extensions from peers").isGreaterThan(0.0)
            val p99ok = histogramShareBelow(m, "beacon_extend_vote_duration_seconds", "0.05")
            assertThat(p99ok).describedAs("${pair.name} share of ExtendVote calls under 50 ms").isGreaterThanOrEqualTo(0.99)
        }

        logSection("Block time with partials vs empty extensions")
        val before = meanBlockInterval(cluster.genesis, enableHeight + 2, minOf(enableHeight + 22, keyedFrom - 1))
        val tip = cluster.genesis.getCurrentBlockHeight()
        val after = meanBlockInterval(cluster.genesis, tip - 20, tip)
        Logger.info("Mean block interval: empty extensions ${before}s, with partials ${after}s")
        assertThat(after).describedAs("mean block interval with partials (empty-extension baseline ${before}s)")
            .isLessThanOrEqualTo(before * 1.25 + 0.5)

        assertNoConsensusFailure(cluster.allPairs)
    }

    // --- helpers ------------------------------------------------------------

    // --- Step 3 helpers ---------------------------------------------------------

    private data class BeaconKeyResponse(val keyEpoch: Long? = null, val keySet: Boolean? = null, val activated: Boolean? = null)
    private data class PendingBeacon(val round: Long? = null, val keyEpoch: Long? = null, val iTotalSlots: Long? = null)
    private data class PendingBeaconResponse(val pending: PendingBeacon? = null)

    private fun queryBeaconKey(pair: LocalInferencePair): BeaconKeyResponse =
        pair.node.execAndParse<BeaconKeyResponse>(listOf("query", "bls", "beacon-key"))

    private fun queryPendingBeacon(pair: LocalInferencePair, round: Long): PendingBeacon? =
        runCatching { pair.node.execAndParse<PendingBeaconResponse>(listOf("query", "bls", "pending-beacon", round.toString())).pending }.getOrNull()

    /** The first beacon key epoch whose DKG includes every validator as a participant. */
    private fun waitForBeaconKeyOverAllValidators(cluster: LocalCluster, maxBlocks: Int = 400): Long {
        val genesis = cluster.genesis
        repeat(maxBlocks) {
            val key = runCatching { queryBeaconKey(genesis) }.getOrNull()
            if (key?.keySet == true && key.keyEpoch != null) {
                val data = runCatching { genesis.node.queryBLSEpochData(key.keyEpoch).epochData }.getOrNull()
                if (data != null && data.participants.size >= cluster.allPairs.size) return key.keyEpoch
            }
            genesis.node.waitForNextBlock(1)
        }
        error("no beacon key over all ${cluster.allPairs.size} validators within $maxBlocks blocks")
    }

    /** Prometheus text from the node's :26660, as series (name{labels}) -> value. */
    private fun nodeMetrics(pair: LocalInferencePair): Map<String, Double> {
        val text = docker("exec", nodeContainer(pair), "wget", "-qO-", "http://127.0.0.1:26660/metrics")
        return text.lines().filter { it.startsWith("beacon_") }.mapNotNull { line ->
            val i = line.lastIndexOf(' ')
            if (i <= 0) null else line.substring(0, i) to (line.substring(i + 1).toDoubleOrNull() ?: return@mapNotNull null)
        }.toMap()
    }

    /** A series key as Prometheus prints it: labels sorted by name. */
    private fun series(name: String, vararg labels: Pair<String, String>): String =
        if (labels.isEmpty()) name
        else name + labels.sortedBy { it.first }.joinToString(",", "{", "}") { "${it.first}=\"${it.second}\"" }

    private fun metric(m: Map<String, Double>, name: String, vararg labels: Pair<String, String>): Double =
        m[series(name, *labels)] ?: 0.0

    private fun rejects(m: Map<String, Double>): Double =
        m.filterKeys { it.startsWith("beacon_verify_vote_ext_total{") && it.contains("result=\"reject\"") }.values.sum()

    /** Fraction of histogram observations at or below bucket le. */
    private fun histogramShareBelow(m: Map<String, Double>, name: String, le: String): Double {
        val count = metric(m, "${name}_count")
        if (count == 0.0) return 0.0
        return metric(m, "${name}_bucket", "le" to le) / count
    }

    /** Mean seconds between consecutive block times over [from, to]. */
    private fun meanBlockInterval(pair: LocalInferencePair, from: Long, to: Long): Double {
        require(to > from) { "empty block window [$from, $to]" }
        fun time(h: Long): java.time.Instant {
            val json = docker("exec", nodeContainer(pair), "wget", "-qO-", "http://127.0.0.1:26657/block?height=$h")
            val t = Regex("\"time\"\\s*:\\s*\"([^\"]+)\"").find(json)?.groupValues?.get(1) ?: error("no time in block $h")
            return java.time.Instant.parse(t)
        }
        return java.time.Duration.between(time(from), time(to)).toMillis() / 1000.0 / (to - from)
    }

    // --- share bridge helpers (Step 2) ---------------------------------------

    private data class ShareFileInfo(val participant: String, val slotStart: Int, val slotEnd: Int, val sha256: String)

    private fun containerBase(pair: LocalInferencePair) = pair.name.trimStart('/')
    private fun nodeContainer(pair: LocalInferencePair) = "${containerBase(pair)}-node"
    private fun sharePath(epochId: Long) = "/root/.inference/bls-beacon/epoch-$epochId.json"

    private data class CurrentEpochResponse(val epoch: Long)

    /**
     * The chain's effective epoch, which is also the BLS signing epoch: ExtendVote asks the
     * share provider for it and the dAPI reconcile loop treats it as "current". Not
     * latestEpoch.index, which advances at PoC start, before that epoch's DKG completes.
     */
    private fun signingEpoch(cluster: LocalCluster): Long =
        cluster.genesis.node.execAndParse<CurrentEpochResponse>(listOf("query", "inference", "get-current-epoch")).epoch

    /** The first signing epoch whose DKG completed with every validator as a participant. */
    private fun waitForSignedShareEpoch(cluster: LocalCluster): Long {
        val genesis = cluster.genesis
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        repeat(300) {
            val current = runCatching { signingEpoch(cluster) }.getOrDefault(0L)
            val data = runCatching { genesis.node.queryBLSEpochData(current).epochData }.getOrNull()
            if (data != null && data.participants.size >= cluster.allPairs.size &&
                (data.dkgPhase.contains("COMPLETED") || data.dkgPhase.contains("SIGNED")) &&
                !data.groupPublicKey.isNullOrBlank()
            ) {
                return current
            }
            genesis.node.waitForNextBlock(1)
        }
        error("no signing epoch with a completed DKG over all ${cluster.allPairs.size} validators")
    }

    /** The share dir is its own tmpfs mount (never on disk, not part of the bind-mounted home). */
    private fun assertShareDirIsTmpfs(container: String) {
        val mounts = docker("exec", container, "cat", "/proc/mounts").lines()
        val entry = mounts.firstOrNull { it.split(" ").getOrNull(1) == "/root/.inference/bls-beacon" }
        assertThat(entry).describedAs("$container mount of /root/.inference/bls-beacon").isNotNull
        assertThat(entry!!.split(" ")[2]).describedAs("$container bls-beacon filesystem").isEqualTo("tmpfs")
    }

    /** Overwrites [to]'s share file with [from]'s, container to container (the secret never reaches the test log). */
    private fun copyShareFile(from: LocalInferencePair, to: LocalInferencePair, epochId: Long) {
        val path = sharePath(epochId)
        val pipeline = "docker exec ${nodeContainer(from)} cat $path | " +
            "docker exec -i ${nodeContainer(to)} sh -c 'umask 077 && cat > $path.copy && mv $path.copy $path'"
        val process = ProcessBuilder("sh", "-c", pipeline).redirectErrorStream(true).start()
        val out = process.inputStream.bufferedReader().readText()
        check(process.waitFor(60, TimeUnit.SECONDS) && process.exitValue() == 0) { "copying the share file failed: $out" }
    }

    /** Waits for `beacon shares loaded … valid=false` on [pair] and returns that line. */
    private fun waitForShareRejection(pair: LocalInferencePair, epochId: Long, maxBlocks: Int = 30): String {
        repeat(maxBlocks) {
            shareLogLines(pair, epochId, valid = false).lastOrNull()?.let { return it }
            pair.node.waitForNextBlock(1)
        }
        error("${pair.name}: foreign share file for epoch $epochId was not rejected within $maxBlocks blocks")
    }

    private fun shareFileExists(pair: LocalInferencePair, epochId: Long): Boolean =
        runCatching { docker("exec", nodeContainer(pair), "test", "-f", sharePath(epochId)); true }.getOrDefault(false)

    /** Mode 0600 in a 0700 directory, parseable; returns the non-secret fields only. */
    private fun assertShareFile(pair: LocalInferencePair, epochId: Long): ShareFileInfo {
        val container = nodeContainer(pair)
        val deadline = System.currentTimeMillis() + 120_000
        while (!shareFileExists(pair, epochId)) {
            check(System.currentTimeMillis() < deadline) { "${pair.name}: ${sharePath(epochId)} missing" }
            Thread.sleep(2_000)
        }
        val fileMode = docker("exec", container, "stat", "-c", "%a", sharePath(epochId)).trim()
        val dirMode = docker("exec", container, "stat", "-c", "%a", "/root/.inference/bls-beacon").trim()
        assertThat(fileMode).describedAs("${pair.name} share file mode").isEqualTo("600")
        assertThat(dirMode).describedAs("${pair.name} share dir mode").isEqualTo("700")

        // Read without logging: the file holds secret shares.
        val json = docker("exec", container, "cat", sharePath(epochId))
        fun str(key: String) = Regex("\"$key\"\\s*:\\s*\"([^\"]*)\"").find(json)?.groupValues?.get(1)
            ?: error("${pair.name}: share file has no $key")
        fun num(key: String) = Regex("\"$key\"\\s*:\\s*(\\d+)").find(json)?.groupValues?.get(1)?.toInt()
            ?: error("${pair.name}: share file has no $key")
        assertThat(num("version")).isEqualTo(1)
        assertThat(num("epoch_id").toLong()).isEqualTo(epochId)
        assertThat(str("chain_id")).isNotBlank()
        assertThat(str("group_public_key")).hasSize(192)
        return ShareFileInfo(str("participant_address"), num("slot_start_index"), num("slot_end_index"), str("sha256"))
    }

    private val ansi = Regex("\u001B\\[[0-9;]*m")

    /** Node log without console colors (`key=value` fields, sorted by key). */
    private fun nodeLogLines(pair: LocalInferencePair): List<String> =
        pair.readSiblingLogs("node", tail = 0).replace(ansi, "").lines()

    /** Node log lines `beacon shares loaded` for an epoch with the given verdict. */
    private fun shareLogLines(pair: LocalInferencePair, epochId: Long, valid: Boolean): List<String> =
        nodeLogLines(pair).filter {
            it.contains("beacon shares loaded") && it.contains("epoch=$epochId ") && it.contains("valid=$valid")
        }

    /** dAPI log without console colors. */
    private fun apiLogLines(pair: LocalInferencePair): List<String> =
        pair.readSiblingLogs("api", tail = 0).replace(ansi, "").lines()

    private fun waitForSharesLoaded(pair: LocalInferencePair, epochId: Long, minCount: Int, maxBlocks: Int = 200) {
        repeat(maxBlocks) {
            if (shareLogLines(pair, epochId, valid = true).size >= minCount) return
            pair.node.waitForNextBlock(1)
        }
        val tail = nodeLogLines(pair).filter { it.contains("beacon") }.takeLast(20)
        error("${pair.name}: no `beacon shares loaded epoch=$epochId valid=true` (x$minCount) within $maxBlocks blocks; beacon log tail:\n${tail.joinToString("\n")}")
    }

    private fun tmkmsConfig(image: String, tmkmsPairs: List<String>): ApplicationConfig =
        veConfig.copy(
            additionalDockerFilesByKeyName = tmkmsPairs.associateWith { listOf("docker-compose.tmkms.yml") },
            additionalEnvVars = veConfig.additionalEnvVars + ("TMKMS_IMAGE" to image),
        )

    private fun assertGenesisEnableHeight(cluster: LocalCluster) {
        cluster.allPairs.forEach { pair ->
            val height = pair.node.getGenesisState().voteExtensionsEnableHeight
            assertThat(height).describedAs("${pair.name} genesis vote_extensions_enable_height").isEqualTo(enableHeight)
        }
    }

    private fun waitPastEnableHeight(pair: LocalInferencePair, blocksAfter: Long): Long =
        pair.node.waitForMinimumBlock(enableHeight + blocksAfter, "past vote-extension enable height")

    /**
     * Every active validator's precommit (with its extension signature, since
     * extensions are on) lands in most recent LastCommits: no validator panics
     * or is silently dropped because its signer cannot sign extensions.
     */
    private fun assertAllValidatorsSigning(cluster: LocalCluster, tip: Long, window: Int) {
        val from = maxOf(enableHeight + 1, tip - window + 1)
        // Block h's LastCommit carries precommits for h-1 from the set at h-1.
        // Only validators active over the whole window can be held to it
        // (join nodes enter the set mid-run).
        fun setAt(h: Long) = cluster.genesis.node.getCometValidatorsAt(h).validators.map { toHexAddress(it.address) }.toSet()
        val validators = setAt(from - 1) intersect setAt(tip - 1)
        assertThat(validators).describedAs("validators active over blocks $from..$tip").isNotEmpty()

        val signersByHeight = (from..tip).associateWith { cluster.genesis.node.getLastCommitSigners(it) }
        validators.forEach { v ->
            val present = signersByHeight.values.count { v in it }
            assertThat(present.toDouble() / signersByHeight.size)
                .describedAs("validator $v precommit participation in blocks $from..$tip")
                .isGreaterThanOrEqualTo(0.8)
        }
    }

    private fun assertParticipation(cluster: LocalCluster, validator: String, from: Long, to: Long, label: String) {
        val heights = (maxOf(1L, from)..to).toList()
        val present = heights.count { validator in cluster.genesis.node.getLastCommitSigners(it) }
        assertThat(present.toDouble() / heights.size)
            .describedAs("$label: validator $validator precommit participation in blocks ${heights.first()}..$to")
            .isGreaterThanOrEqualTo(0.8)
    }

    private fun tmkmsStateHeight(container: String): Long {
        val json = docker("exec", container, "cat", "/root/.tmkms/state/priv_validator_state.json")
        return Regex("\"height\"\\s*:\\s*\"?(\\d+)\"?").find(json)?.groupValues?.get(1)?.toLong()
            ?: error("cannot parse tmkms state height from: $json")
    }

    private fun docker(vararg args: String): String {
        val process = ProcessBuilder(listOf("docker") + args).redirectErrorStream(true).start()
        val out = process.inputStream.bufferedReader().readText()
        check(process.waitFor(120, TimeUnit.SECONDS) && process.exitValue() == 0) {
            "docker ${args.joinToString(" ")} failed: $out"
        }
        return out
    }

    private fun assertNoConsensusFailure(pairs: List<LocalInferencePair>) {
        pairs.forEach { pair ->
            val logs = pair.readSiblingLogs("node", tail = 3000)
            assertThat(logs).describedAs("${pair.name} node logs").doesNotContain("CONSENSUS FAILURE")
            assertThat(logs).describedAs("${pair.name} node logs").doesNotContain("ExtendVote call failed")
            assertThat(logs).describedAs("${pair.name} node logs").doesNotContain("VerifyVoteExtension call failed")
        }
    }

    private fun waitUntilCometValidator(pair: LocalInferencePair, maxBlocks: Int = 200) {
        val address = toHexAddress(pair.node.getStatus().validatorInfo.address)
        pair.waitForBlock(maxBlocks) {
            it.node.getCometValidators().validators.any { v -> toHexAddress(v.address) == address }
        }
    }

    /** `comet-validator-set` reports bech32 valcons addresses; `status` reports hex. */
    private fun toHexAddress(addr: String): String =
        if (addr.contains("valcons")) bech32ToHex(addr) else normalizeValidatorAddress(addr)

    private fun bech32ToHex(addr: String): String {
        val charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
        val data = addr.substringAfterLast('1').dropLast(6).map { charset.indexOf(it) }
        var acc = 0
        var bits = 0
        val out = mutableListOf<Int>()
        for (v in data) {
            acc = (acc shl 5) or v
            bits += 5
            while (bits >= 8) {
                bits -= 8
                out += (acc shr bits) and 0xff
            }
        }
        return out.joinToString("") { "%02X".format(it) }
    }
}
