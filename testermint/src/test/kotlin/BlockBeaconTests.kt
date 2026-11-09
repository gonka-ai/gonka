import com.productscience.ApplicationConfig
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
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
 * Block BLS beacon, Step 1 (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md):
 * CometBFT vote extensions enabled with empty extensions.
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

    // --- helpers ------------------------------------------------------------

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
