import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.api.model.Frame
import com.github.dockerjava.core.DockerClientBuilder
import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import kotlin.test.assertNotNull
import org.assertj.core.api.Assertions.assertThat
import java.time.Duration

private val devshardWarmupTimeout: Duration = Duration.ofMinutes(2)
private const val devshardActivePhase = "active"
private const val devshardWarmupProbeNonce = 1L
private const val devshardStatusUnavailable = "status-unavailable"
private val devshardWarmupPollInterval: Duration = Duration.ofMillis(500)
private val devshardPreFinalizeDelay: Duration = Duration.ofSeconds(2)
private const val versionedMlNodeSegment = "v3.0.8"

val devshardNoRestrictionsSpec = spec<AppState> {
    this[AppState::restrictions] = spec<RestrictionsState> {
        this[RestrictionsState::params] = spec<RestrictionsParams> {
            this[RestrictionsParams::restrictionEndBlock] = 0L
            this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>()
            this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>()
        }
    }
}

val devshardAlwaysValidateSpec = spec<AppState> {
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::validationParams] = spec<ValidationParams> {
                this[ValidationParams::minValidationAverage] = Decimal.fromDouble(100.0)
                this[ValidationParams::maxValidationAverage] = Decimal.fromDouble(100.0)
                this[ValidationParams::downtimeHThreshold] = Decimal.fromDouble(100.0)
            }
            this[InferenceParams::bandwidthLimitsParams] = spec<BandwidthLimitsParams> {
                this[BandwidthLimitsParams::minimumConcurrentInvalidations] = 100L
            }
        }
    }
}

/** Short seal grace for gateway auto-seal tests (Finished inferences + wall-clock gate). */
const val devshardAutoSealGroupSize = 16L
const val devshardAutoSealInferenceSealGraceNonces = 1L
const val devshardAutoSealInferenceSealGraceSeconds = 10L
/**
 * Finished clock gate is grace_seconds + execution_timeout. Keep this small so
 * the testermint sleep can actually clear the Finished gate (default is 1920s).
 */
const val devshardAutoSealExecutionTimeout = 2L
/** Must match devshardShortSealGraceSpec default_auto_seal_every_n_nonces. */
const val devshardAutoSealEveryNNonces = 1L

val devshardShortSealGraceSpec = spec<AppState> {
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                this[DevshardEscrowParams::defaultInferenceSealGraceNonces] = devshardAutoSealInferenceSealGraceNonces
                this[DevshardEscrowParams::defaultInferenceSealGraceSeconds] = devshardAutoSealInferenceSealGraceSeconds
                this[DevshardEscrowParams::defaultAutoSealEveryNNonces] = devshardAutoSealEveryNNonces
                this[DevshardEscrowParams::executionTimeout] = devshardAutoSealExecutionTimeout
                // Do not set validationRate=0: chain treats 0 as unset and snapshots
                // DefaultDevshardValidationRate (1000 = 10%).
            }
        }
    }
}

/** 100% devshard validation sampling (basis points). Distinct from legacy ValidationParams above. */
val devshardEscrowAlwaysValidateSpec = spec<AppState> {
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                this[DevshardEscrowParams::validationRate] = 10_000L
            }
        }
    }
}

/** 50% devshard escrow validation sampling (basis points). */
val devshardEscrowHalfValidateSpec = spec<AppState> {
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                this[DevshardEscrowParams::validationRate] = 5_000L
            }
        }
    }
}

fun LocalInferencePair.traceDevshardInferencePhase(
    handle: LocalInferencePair.DevshardEscrowRoute,
    inferenceId: Long,
    label: String,
) {
    runCatching {
        val inference = getDevshardEscrowInferences(handle.escrowUrl)[inferenceId]
        logSection("phase-trace [$label] inference_id=$inferenceId proxy_state=${inference?.let { cosmosJson.toJson(it) } ?: "missing"}")
    }.onFailure {
        logSection("phase-trace [$label] inference_id=$inferenceId proxy_state=unavailable (${it.message})")
    }
}

fun LocalInferencePair.dumpDevshardChallengeTraceLogs(escrowId: Long) {
    val patterns = listOf(
        "execute_ml_",
        "validation_ml_",
        "validation_enqueued",
        "apply_validation",
        "proxy_inference_",
        "validation started",
        "validation_result",
        "validation_vote",
    )
    val grepExpr = patterns.joinToString("|") { Regex.escape(it) }
    logSection("phase-trace docker logs (escrow=$escrowId, patterns=$grepExpr)")
    runCatching {
        val dockerClient = DockerClientBuilder.getInstance().build()
        listOf("genesis", "join1", "join2").forEach { name ->
            listOf("api", "proxy").forEach { svc ->
                val containerName = "$name-$svc"
                val collector = StringBuilder()
                dockerClient.logContainerCmd(containerName)
                    .withStdOut(true)
                    .withStdErr(true)
                    .withTail(800)
                    .exec(
                        object : ResultCallback.Adapter<Frame>() {
                            override fun onNext(item: Frame) {
                                collector.append(item.toString())
                            }
                        },
                    )
                    .awaitCompletion()
                val filtered = collector.lines()
                    .filter { line -> patterns.any { p -> line.contains(p) } }
                    .joinToString("\n")
                logSection("phase-trace $containerName (${filtered.lines().size} matching lines):\n$filtered")
            }
        }
        runCatching {
            val lines = api.executor.exec(
                listOf("sh", "-c", "grep -E '$grepExpr' $gatewayLogPath 2>/dev/null | tail -200 || true"),
                null,
            )
            logSection("phase-trace gateway ($gatewayLogPath):\n${lines.joinToString("\n")}")
        }.onFailure {
            logSection("phase-trace gateway log unavailable: ${it.message}")
        }
    }.onFailure {
        logSection("phase-trace docker dump failed: ${it.message}")
    }
}

data class DevshardTestUser(
    val keyName: String,
    val address: String,
    val fundAmount: Long,
)

/**
 * Wait until the chain is in [EpochPhase.Inference] with enough blocks after PoC start
 * and before the next PoC start. Avoids starting runtime-config long-polls right before
 * an epoch transition (which wakes waiters unpredictably).
 *
 * Uses [EpochResponse.nextEpochStages.pocStart] like [EpochResponse.safeForInference].
 */
fun LocalInferencePair.waitForMidEpochWindow(
    minBlocksIntoEpoch: Long = 3,
    minBlocksBeforeNextPoc: Long = 4,
) {
    repeat(60) {
        val epoch = getEpochData()
        val height = getCurrentBlockHeight()
        val pocStart = epoch.latestEpoch.pocStartBlockHeight
        val nextPocStart = epoch.nextEpochStages.pocStart
        val blocksInto = height - pocStart
        val blocksUntilNextPoc = nextPocStart - height
        if (epoch.phase == EpochPhase.Inference &&
            blocksInto >= minBlocksIntoEpoch &&
            blocksUntilNextPoc >= minBlocksBeforeNextPoc
        ) {
            return
        }
        Thread.sleep(2_000)
    }
    val epoch = getEpochData()
    val height = getCurrentBlockHeight()
    val pocStart = epoch.latestEpoch.pocStartBlockHeight
    val nextPocStart = epoch.nextEpochStages.pocStart
    val blocksInto = height - pocStart
    val blocksUntilNextPoc = nextPocStart - height
    error(
        "timed out waiting for mid-epoch window: height=$height phase=${epoch.phase} " +
            "pocStart=$pocStart nextPocStart=$nextPocStart blocksInto=$blocksInto " +
            "blocksUntilNextPoc=$blocksUntilNextPoc",
    )
}

/** Waits until the gateway's warm-up probe has taken the escrow's first nonce, which it does only after the escrow's height seed is ready. */
fun LocalInferencePair.waitForDevshardEscrowWarmup(
    escrowUrl: String,
    timeout: Duration = devshardWarmupTimeout,
) {
    logSection("Waiting for the gateway's warm-up probe on $escrowUrl")
    val deadline = System.currentTimeMillis() + timeout.toMillis()
    var last = devshardStatusUnavailable
    while (System.currentTimeMillis() < deadline) {
        val status = runCatching { getDevshardEscrowStatus(escrowUrl) }.getOrNull()
        if (status != null && status.phase == devshardActivePhase && status.nonce >= devshardWarmupProbeNonce) {
            return
        }
        last = status?.let { "phase=${it.phase} nonce=${it.nonce}" } ?: devshardStatusUnavailable
        Thread.sleep(devshardWarmupPollInterval.toMillis())
    }
    error("timed out waiting for the gateway's warm-up probe on $escrowUrl (last $last)")
}

/**
 * Poll chat completions until versiond returns HTTP 503 (devshard requests disabled on chain).
 * Use after a governance proposal that sets devshard_requests_enabled=false.
 */
fun LocalInferencePair.waitForDevshardCompletionRejected(
    escrowModelId: String,
    escrowUrl: String,
    timeoutMs: Long = 60_000L,
    pollIntervalMs: Long = 2_000L,
    requestTimeoutSeconds: Int = 8,
): Boolean {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val resp = try {
            sendChatCompletionWithStatus(
                escrowUrl,
                escrowModelId,
                "devshard-rejected-poll",
                maxTimeSeconds = requestTimeoutSeconds,
            )
        } catch (e: IllegalStateException) {
            // PoC / ML backlog can hang completions; connection errors after dapi restart — retry.
            if (e.message?.contains("curl") == true) {
                null
            } else {
                throw e
            }
        }
        if (resp?.httpCode == 503) {
            return true
        }
        Thread.sleep(pollIntervalMs)
    }
    return false
}

fun LocalInferencePair.waitForDevshardPreFinalize(delay: Duration = devshardPreFinalizeDelay) {
    logSection("Waiting before finalization")
    Thread.sleep(delay.toMillis())
}

/** Sends catch-up diffs to the hosts that have not signed the current nonce, through the gateway's signature collection. */
fun LocalInferencePair.syncDevshardEscrowHosts(escrowUrl: String, log: Boolean = true) {
    if (log) {
        logSection("Collecting signatures (catches up lagging hosts)")
    }
    val nonce = getDevshardEscrowStatus(escrowUrl).nonce
    val raw = api.executor.exec(
        listOf(
            "sh", "-c",
            "curl -sf -X POST '$escrowUrl/v1/debug/signatures/collect?nonce=$nonce' " +
                "-H 'Authorization: Bearer $devshardAdminApiKey'",
        ),
        null,
    ).joinToString("")
    check(raw.contains('{') && raw.contains('}')) {
        "signatures/collect returned no JSON object. raw:\n$raw"
    }
}

fun IInferenceMock.stubDevshardResponseForAllSegments(
    response: String,
    delay: Duration = Duration.ZERO,
    streamDelay: Duration = Duration.ZERO,
    model: String? = null,
    hostName: String? = null,
) {
    listOf("", versionedMlNodeSegment).forEach { segment ->
        setInferenceResponse(
            response = response,
            delay = delay,
            streamDelay = streamDelay,
            segment = segment,
            model = model,
            hostName = hostName,
        )
    }
}

fun IInferenceMock.stubDevshardResponseForAllSegments(
    response: OpenAIResponse,
    delay: Duration = Duration.ZERO,
    streamDelay: Duration = Duration.ZERO,
    model: String? = null,
    hostName: String? = null,
) {
    listOf("", versionedMlNodeSegment).forEach { segment ->
        setInferenceResponse(
            openAIResponse = response,
            delay = delay,
            streamDelay = streamDelay,
            segment = segment,
            model = model,
            hostName = hostName,
        )
    }
}

fun LocalCluster.stubDevshardChatResponse(
    content: String = "hello",
    streamDelay: Duration = Duration.ZERO,
) {
    val response = defaultInferenceResponseObject.withResponse(content)
    allPairs.forEach { pair ->
        pair.mock?.stubDevshardResponseForAllSegments(
            response = response,
            streamDelay = streamDelay,
        )
    }
}

/**
 * Genesis cold wallet starts with little liquid ngonka; rewards arrive at [EpochStage.CLAIM_REWARDS].
 * [waitForNextInferenceWindow] often skips that stage mid-epoch, so fund users only after balance is sufficient.
 */
fun LocalInferencePair.ensureGenesisSpendableForDevshard(
    minBalance: Long,
    maxAttempts: Int = 4,
) {
    repeat(maxAttempts) { attempt ->
        val balance = node.getSelfBalance(config.denom)
        if (balance >= minBalance) {
            return
        }
        logSection(
            "Genesis balance $balance < $minBalance; waiting for CLAIM_REWARDS " +
                "(attempt ${attempt + 1}/$maxAttempts)",
        )
        waitForStage(EpochStage.CLAIM_REWARDS)
        Thread.sleep(2_000)
    }
    val balance = node.getSelfBalance(config.denom)
    check(balance >= minBalance) {
        "Genesis cold account needs at least $minBalance${config.denom} for bank send; balance=$balance"
    }
}

fun LocalInferencePair.createFundedDevshardUser(
    userKeyName: String,
    fundAmount: Long = 10_000_000_000L,
): DevshardTestUser {
    ensureGenesisSpendableForDevshard(fundAmount)
    logSection("Creating separate user account")
    val userKey = node.createKey(userKeyName)
    val transferResp = submitTransaction(
        listOf("bank", "send", node.getColdAddress(), userKey.address, "${fundAmount}${config.denom}")
    )
    assertThat(transferResp.code).isEqualTo(0)
    return DevshardTestUser(
        keyName = userKeyName,
        address = userKey.address,
        fundAmount = fundAmount,
    )
}

fun LocalInferencePair.createDevshardEscrowForUser(
    escrowAmount: Long,
    userKeyName: String,
    modelId: String,
): Long {
    logSection("Creating devshard escrow from user account")
    val txResp = createDevshardEscrow(escrowAmount, from = userKeyName, modelId = modelId)
    assertThat(txResp.code).isEqualTo(0)
    return txResp.getEscrowId() ?: 1L
}

fun LocalInferencePair.assertDevshardSettlement(
    handle: LocalInferencePair.DevshardEscrowRoute,
    escrowId: Long,
    user: DevshardTestUser,
    escrowAmount: Long,
    requireCompletedValidations: Boolean = false,
    expectedStateRootProtocolVersion: String = devshardStateRootProtocolVersion(),
): LocalInferencePair.GatewaySettlementResult {
    waitForDevshardPreFinalize()
    val statusBeforeFinalization = getDevshardEscrowStatus(handle.escrowUrl)
    logSection("Settling through the gateway")
    val result = settleDevshardThroughGateway(escrowId)
    val settlement = result.settlement
    val settleTransaction = result.settleTx
    assertThat(settleTransaction.code).isEqualTo(0)
    assertThat(result.settler).isEqualTo(user.address)

    logSection("Verifying settlement data")
    assertThat(settlement.escrowId).isEqualTo(escrowId.toString())
    assertThat(settlement.stateRootAndProtocolVersion).isEqualTo(expectedStateRootProtocolVersion)
    assertThat(settlement.nonce).isGreaterThan(0)
    assertThat(settlement.hostStats).isNotEmpty()
    assertThat(settlement.signatures).isNotEmpty()

    val activeNonces = statusBeforeFinalization.nonce
    val expectedFees =
        statusBeforeFinalization.config.createDevshardFee +
            (statusBeforeFinalization.config.feePerNonce * activeNonces)
    assertThat(settlement.nonce).isGreaterThanOrEqualTo(activeNonces)
    assertThat(settlement.fees).isEqualTo(expectedFees)

    val totalCompletedValidations = settlement.hostStats.sumOf { it.completedValidations ?: 0 }
    if (requireCompletedValidations) {
        assertThat(totalCompletedValidations).isGreaterThan(0)
    }

    val totalCost = settlement.hostStats.sumOf { it.cost }
    val totalPayout = totalCost + settlement.fees
    val expectedRemainder = escrowAmount - totalPayout

    val settleEvent = assertNotNull(settleTransaction.events.firstOrNull { it.type == "devshard_escrow_settled" })
    assertThat(settleEvent.attributes.firstOrNull { it.key == "total_payout" }?.value)
        .isEqualTo(totalPayout.toString())
    assertThat(settleEvent.attributes.firstOrNull { it.key == "fees" }?.value)
        .isEqualTo(settlement.fees.toString())
    assertThat(settleEvent.attributes.firstOrNull { it.key == "remainder" }?.value)
        .isEqualTo(expectedRemainder.toString())
    assertThat(
        settleEvent.attributes.firstOrNull { it.key == "state_root_and_protocol_version" }?.value,
    ).isEqualTo(expectedStateRootProtocolVersion)

    logSection("Verifying escrow settled")
    val escrow = node.queryDevshardEscrow(escrowId)
    assertThat(escrow.escrow!!.settled).isTrue()

    logSection("Verifying user got refund")
    val balanceAfter = getBalance(user.address)
    assertThat(balanceAfter).isEqualTo(user.fundAmount - totalPayout)

    return result
}

fun LocalInferencePair.getDevshardShardStatsDetail(
    escrowId: Long,
    routePrefix: String = com.productscience.defaultDevshardRoutePrefix(),
): DevshardShardStatsDetail {
    val normalizedPrefix = routePrefix.trimEnd('/')
    val path = "$normalizedPrefix/stats/shards/$escrowId"
    val raw = if (normalizedPrefix.startsWith("/devshard/")) {
        val url = "${api.getPublicUrl().trimEnd('/')}$path"
        val (_, response, result) = Fuel.get(url).timeoutRead(10_000).responseString()
        check(response.statusCode == 200) {
            "GET $url returned ${response.statusCode}: $result"
        }
        result.get()
    } else {
        curlFromApiNetwork("${apiContainerPublicUrl()}$path")
    }
    return cosmosJson.fromJson(raw, DevshardShardStatsDetail::class.java)
}

fun LocalInferencePair.waitForDevshardValidationObservability(
    escrowId: Long,
    minCompleted: Int = 1,
    timeoutMs: Long = 120_000L,
    pollIntervalMs: Long = 2_000L,
    routePrefix: String = com.productscience.defaultDevshardRoutePrefix(),
    /**
     * Invoked after an incomplete/failed poll and before sleeping. Use to
     * re-run [syncDevshardEscrowHosts] so late validation diffs from join hosts
     * land on the pair whose stats endpoint is being polled.
     */
    beforeRetry: (() -> Unit)? = null,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    var lastError: Throwable? = null
    var lastCompleted: Int? = null
    while (System.currentTimeMillis() < deadline) {
        // Treat 404 / transient resolve failures as "not ready yet". Parallel
        // sessions can lag on genesis versiond's active-session list (same class
        // of flake that sync-hosts was added for on local-build-pixelplex).
        try {
            val stats = getDevshardShardStatsDetail(escrowId, routePrefix)
            lastError = null
            lastCompleted = stats.validationObservability.totals.completedValidations
            if (lastCompleted >= minCompleted) {
                return
            }
        } catch (t: Throwable) {
            lastError = t
            lastCompleted = null
        }
        beforeRetry?.invoke()
        Thread.sleep(pollIntervalMs)
    }
    val shardsList = runCatching { getDevshardShardsStatsList(routePrefix) }.getOrElse { e ->
        "GET stats/shards failed: ${e.message}"
    }
    val detail = when {
        lastError != null -> "last error: ${lastError.message}"
        lastCompleted != null -> "got $lastCompleted"
        else -> "no successful stats response"
    }
    error(
        "timed out waiting for validation observability completed >= $minCompleted " +
            "for escrow $escrowId ($detail); stats/shards=$shardsList",
    )
}

/** List endpoint used to diagnose detail 404s (current_epoch + active_escrows). */
fun LocalInferencePair.getDevshardShardsStatsList(
    routePrefix: String = com.productscience.defaultDevshardRoutePrefix(),
): String {
    val normalizedPrefix = routePrefix.trimEnd('/')
    val path = "$normalizedPrefix/stats/shards"
    return if (normalizedPrefix.startsWith("/devshard/")) {
        val url = "${api.getPublicUrl().trimEnd('/')}$path"
        val (_, response, result) = Fuel.get(url).timeoutRead(10_000).responseString()
        check(response.statusCode == 200) {
            "GET $url returned ${response.statusCode}: $result"
        }
        result.get()
    } else {
        curlFromApiNetwork("${apiContainerPublicUrl()}$path")
    }
}

/** True when validation challenged the inference and/or quorum invalidated it. */
fun DevshardInferencePayload.hasChallengedOutcome(): Boolean =
    status == DevshardInferenceStatus.CHALLENGED || status == DevshardInferenceStatus.INVALIDATED

fun LocalInferencePair.findChallengedDevshardInference(
    handle: LocalInferencePair.DevshardEscrowRoute,
): DevshardInferencePayload? {
    return getDevshardEscrowInferences(handle.escrowUrl)
        .values.firstOrNull { it.hasChallengedOutcome() }
}

fun LocalInferencePair.waitForConfirmedDevshardInferences(
    escrowUrl: String,
    minCount: Int,
    timeoutMs: Long = 120_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val inferences = getDevshardEscrowInferences(escrowUrl)
        val confirmed = inferences.values.count { inferenceRecord ->
            inferenceRecord.confirmedAtHeight != null &&
                inferenceRecord.confirmedAtHeight!! > 0 &&
                (
                    inferenceRecord.status == DevshardInferenceStatus.FINISHED ||
                        inferenceRecord.status == DevshardInferenceStatus.STARTED ||
                        inferenceRecord.status == DevshardInferenceStatus.VALIDATED
                    )
        }
        if (confirmed >= minCount) {
            return
        }
        Thread.sleep(pollIntervalMs)
    }
    val inferences = getDevshardEscrowInferences(escrowUrl)
    error(
        "timed out waiting for $minCount confirmed inferences " +
            "(got ${inferences.values.count { it.confirmedAtHeight != null && it.confirmedAtHeight!! > 0 }})",
    )
}

fun LocalInferencePair.waitForFinishedDevshardInferences(
    escrowUrl: String,
    minCount: Int,
    timeoutMs: Long = 120_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val inferences = getDevshardEscrowInferences(escrowUrl)
        val finished = inferences.values.count { inferenceRecord ->
            inferenceRecord.status == DevshardInferenceStatus.FINISHED &&
                inferenceRecord.confirmedAtHeight != null &&
                inferenceRecord.confirmedAtHeight!! > 0
        }
        if (finished >= minCount) {
            return
        }
        Thread.sleep(pollIntervalMs)
    }
    val inferences = getDevshardEscrowInferences(escrowUrl)
    error(
        "timed out waiting for $minCount finished inferences " +
            "(got ${inferences.values.count { it.status == DevshardInferenceStatus.FINISHED }})",
    )
}

fun LocalInferencePair.devshardStatusCounts(escrowUrl: String): Map<DevshardInferenceStatus, Int> =
    getDevshardEscrowInferences(escrowUrl).values.groupingBy { it.status }.eachCount()

/**
 * Drive chat completions until [targetNonce] is reached and at least [minSealed]
 * inferences have been folded into sealed_acc. Auto-seal runs only on nonces that
 * are multiples of [devshardAutoSealEveryNNonces] (escrow snapshot at create).
 */
fun LocalInferencePair.waitForDevshardAutoSeal(
    escrowUrl: String,
    minSealed: Int,
    targetNonce: Long = devshardAutoSealEveryNNonces,
    model: String = defaultModel,
    timeoutMs: Long = 300_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    var drive = 0
    while (System.currentTimeMillis() < deadline) {
        val debug = getDevshardEscrowDebugState(escrowUrl)
        if (debug.nonce >= targetNonce && debug.sealedInferences >= minSealed) {
            return
        }
        if (debug.nonce < targetNonce || debug.sealedInferences < minSealed) {
            val response = sendChatCompletion(escrowUrl, model, "autoseal drive $drive")
            assertThat(response).isNotEmpty()
            drive++
        }
        Thread.sleep(pollIntervalMs)
    }
    val last = getDevshardEscrowDebugState(escrowUrl)
    error(
        "timed out waiting for auto-seal: nonce=${last.nonce} target=$targetNonce " +
            "sealed=${last.sealedInferences} minSealed=$minSealed",
    )
}
