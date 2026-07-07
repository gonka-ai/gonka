import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.api.model.Frame
import com.github.dockerjava.core.DockerClientBuilder
import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import kotlin.test.assertNotNull
import org.assertj.core.api.Assertions.assertThat
import java.time.Duration

private val devshardProxyWarmupDelay: Duration = Duration.ofSeconds(2)
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
/** Must match devshardShortSealGraceSpec default_auto_seal_every_n_nonces. */
const val devshardAutoSealEveryNNonces = 1L

val devshardShortSealGraceSpec = spec<AppState> {
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::devshardEscrowParams] = spec<DevshardEscrowParams> {
                this[DevshardEscrowParams::defaultInferenceSealGraceNonces] = devshardAutoSealInferenceSealGraceNonces
                this[DevshardEscrowParams::defaultInferenceSealGraceSeconds] = devshardAutoSealInferenceSealGraceSeconds
                this[DevshardEscrowParams::defaultAutoSealEveryNNonces] = devshardAutoSealEveryNNonces
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

fun LocalInferencePair.traceDevshardInferencePhase(
    handle: LocalInferencePair.DevshardProxyHandle,
    inferenceId: Long,
    label: String,
) {
    runCatching {
        val inference = getDevshardProxyInferences(handle.proxyUrl)[inferenceId]
        logSection("phase-trace [$label] inference_id=$inferenceId proxy_state=${inference?.let { cosmosJson.toJson(it) } ?: "missing"}")
    }.onFailure {
        logSection("phase-trace [$label] inference_id=$inferenceId proxy_state=unavailable (${it.message})")
    }
}

fun LocalInferencePair.dumpDevshardChallengeTraceLogs(escrowId: Long) {
    val patterns = listOf(
        "execute_ml_",
        "validation_ml_",
        "validation_rate_bound",
        "validation_rate_sample",
        "validation_enqueued",
        "validation_triggered",
        "apply_validation",
        "proxy_inference_",
        "validation started",
        "validation_result",
        "validation_vote",
        "NewStateMachine",
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
        val proxyLog = devshardProxyLogPath(escrowId)
        runCatching {
            val lines = api.executor.exec(
                listOf("sh", "-c", "grep -E '$grepExpr' $proxyLog 2>/dev/null | tail -200 || true"),
                null,
            )
            logSection("phase-trace devshardctl ($proxyLog):\n${lines.joinToString("\n")}")
        }.onFailure {
            logSection("phase-trace devshardctl log unavailable: ${it.message}")
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

fun LocalInferencePair.waitForDevshardProxyWarmup(delay: Duration = devshardProxyWarmupDelay) {
    logSection("Waiting for devshard proxy warmup")
    Thread.sleep(delay.toMillis())
}

/**
 * Poll chat completions until versiond returns HTTP 503 (devshard requests disabled on chain).
 * Use after a governance proposal that sets devshard_requests_enabled=false.
 */
fun LocalInferencePair.waitForDevshardCompletionRejected(
    escrowModelId: String,
    proxyUrl: String,
    timeoutMs: Long = 60_000L,
    pollIntervalMs: Long = 2_000L,
    requestTimeoutSeconds: Int = 8,
): Boolean {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val resp = try {
            sendChatCompletionWithStatus(
                proxyUrl,
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

/** Propagate signed diffs to all physical hosts before observability/finalize. */
fun LocalInferencePair.syncDevshardProxyHosts(proxyUrl: String) {
    logSection("Syncing devshard proxy hosts")
    val raw = api.executor.exec(
        listOf(
            "sh", "-c",
            "curl -sf -X POST $proxyUrl/v1/debug/sync-hosts " +
                "-H 'Authorization: Bearer $devshardAdminApiKey'",
        ),
        null,
    ).joinToString("")
    val start = raw.indexOf('{')
    val end = raw.lastIndexOf('}')
    check(start >= 0 && end >= 0) {
        "sync-hosts returned no JSON object. raw:\n$raw"
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
    handle: LocalInferencePair.DevshardProxyHandle,
    escrowId: Long,
    user: DevshardTestUser,
    escrowAmount: Long,
    requireCompletedValidations: Boolean = false,
    expectedStateRootProtocolVersion: String = devshardStateRootProtocolVersion(),
): LocalInferencePair.DevshardctlResult {
    waitForDevshardPreFinalize()
    logSection("Finalizing via proxy")
    val statusBeforeFinalization = getDevshardProxyStatus(handle.proxyUrl)
    val result = finalizeDevshardProxy(handle.proxyUrl)

    logSection("Verifying settlement data")
    assertThat(result.parsed.escrowId).isEqualTo(escrowId.toString())
    assertThat(result.parsed.stateRootAndProtocolVersion).isEqualTo(expectedStateRootProtocolVersion)
    assertThat(result.parsed.nonce).isGreaterThan(0)
    assertThat(result.parsed.hostStats).isNotEmpty()
    assertThat(result.parsed.signatures).isNotEmpty()

    val activeNonces = statusBeforeFinalization.nonce
    val expectedFees =
        statusBeforeFinalization.config.createDevshardFee +
            (statusBeforeFinalization.config.feePerNonce * activeNonces)
    assertThat(result.parsed.nonce).isGreaterThanOrEqualTo(activeNonces)
    assertThat(result.parsed.fees).isEqualTo(expectedFees)

    val totalCompletedValidations = result.parsed.hostStats.sumOf { it.completedValidations }
    if (requireCompletedValidations) {
        assertThat(totalCompletedValidations).isGreaterThan(0)
    }

    val totalCost = result.parsed.hostStats.sumOf { it.cost }
    val totalPayout = totalCost + result.parsed.fees
    val expectedRemainder = escrowAmount - totalPayout

    logSection("Submitting settlement from user account")
    val settleResp = settleDevshardEscrow(result.rawJson, from = user.keyName)
    assertThat(settleResp.code).isEqualTo(0)

    val settleEvent = assertNotNull(settleResp.events.firstOrNull { it.type == "devshard_escrow_settled" })
    assertThat(settleEvent.attributes.firstOrNull { it.key == "total_payout" }?.value)
        .isEqualTo(totalPayout.toString())
    assertThat(settleEvent.attributes.firstOrNull { it.key == "fees" }?.value)
        .isEqualTo(result.parsed.fees.toString())
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
    routePrefix: String,
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
    routePrefix: String,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val stats = getDevshardShardStatsDetail(escrowId, routePrefix)
        if (stats.validationObservability.totals.completedValidations >= minCompleted) {
            return
        }
        Thread.sleep(pollIntervalMs)
    }
    val last = getDevshardShardStatsDetail(escrowId, routePrefix)
    error(
        "timed out waiting for validation observability completed >= $minCompleted " +
            "(got ${last.validationObservability.totals.completedValidations})",
    )
}

/** True when validation challenged the inference and/or quorum invalidated it. */
fun DevshardInferencePayload.hasChallengedOutcome(): Boolean =
    status == DevshardInferenceStatus.CHALLENGED || status == DevshardInferenceStatus.INVALIDATED

fun LocalInferencePair.findChallengedDevshardInference(
    handle: LocalInferencePair.DevshardProxyHandle,
): DevshardInferencePayload? {
    return getDevshardProxyInferences(handle.proxyUrl)
        .values.firstOrNull { it.hasChallengedOutcome() }
}

fun LocalInferencePair.waitForConfirmedDevshardInferences(
    proxyUrl: String,
    minCount: Int,
    timeoutMs: Long = 120_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val inferences = getDevshardProxyInferences(proxyUrl)
        val confirmed = inferences.values.count { rec ->
            rec.confirmedAt != null &&
                rec.confirmedAt!! > 0 &&
                (
                    rec.status == DevshardInferenceStatus.FINISHED ||
                        rec.status == DevshardInferenceStatus.STARTED ||
                        rec.status == DevshardInferenceStatus.VALIDATED
                    )
        }
        if (confirmed >= minCount) {
            return
        }
        Thread.sleep(pollIntervalMs)
    }
    val inferences = getDevshardProxyInferences(proxyUrl)
    error(
        "timed out waiting for $minCount confirmed inferences " +
            "(got ${inferences.values.count { it.confirmedAt != null && it.confirmedAt!! > 0 }})",
    )
}

fun LocalInferencePair.waitForFinishedDevshardInferences(
    proxyUrl: String,
    minCount: Int,
    timeoutMs: Long = 120_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        val inferences = getDevshardProxyInferences(proxyUrl)
        val finished = inferences.values.count { rec ->
            rec.status == DevshardInferenceStatus.FINISHED &&
                rec.confirmedAt != null &&
                rec.confirmedAt!! > 0
        }
        if (finished >= minCount) {
            return
        }
        Thread.sleep(pollIntervalMs)
    }
    val inferences = getDevshardProxyInferences(proxyUrl)
    error(
        "timed out waiting for $minCount finished inferences " +
            "(got ${inferences.values.count { it.status == DevshardInferenceStatus.FINISHED }})",
    )
}

/**
 * Drive chat completions until [targetNonce] is reached and at least [minSealed]
 * inferences have been folded into sealed_acc. Auto-seal runs only on nonces that
 * are multiples of [devshardAutoSealEveryNNonces] (escrow snapshot at create).
 */
fun LocalInferencePair.waitForDevshardAutoSeal(
    proxyUrl: String,
    minSealed: Int,
    targetNonce: Long = devshardAutoSealEveryNNonces,
    model: String = defaultModel,
    timeoutMs: Long = 300_000L,
    pollIntervalMs: Long = 500L,
) {
    val deadline = System.currentTimeMillis() + timeoutMs
    var drive = 0
    while (System.currentTimeMillis() < deadline) {
        val debug = getDevshardProxyDebugState(proxyUrl)
        if (debug.nonce >= targetNonce && debug.sealedInferences >= minSealed) {
            return
        }
        if (debug.nonce < targetNonce || debug.sealedInferences < minSealed) {
            val response = sendChatCompletion(proxyUrl, model, "autoseal drive $drive")
            assertThat(response).isNotEmpty()
            drive++
        }
        Thread.sleep(pollIntervalMs)
    }
    val last = getDevshardProxyDebugState(proxyUrl)
    error(
        "timed out waiting for auto-seal: nonce=${last.nonce} target=$targetNonce " +
            "sealed=${last.sealedInferences} minSealed=$minSealed",
    )
}
