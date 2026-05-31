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

private fun devshardChatResponse(content: String): String =
    """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"$content"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}"""

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
    allPairs.forEach { pair ->
        pair.mock?.stubDevshardResponseForAllSegments(
            response = devshardChatResponse(content),
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
    requireCompletedValidations: Boolean = true,
    expectedVersion: String? = null,
): LocalInferencePair.DevshardctlResult {
    waitForDevshardPreFinalize()
    logSection("Finalizing via proxy")
    val statusBeforeFinalization = getDevshardProxyStatus(handle.proxyUrl)
    val result = finalizeDevshardProxy(handle.proxyUrl)

    logSection("Verifying settlement data")
    assertThat(result.parsed.escrowId).isEqualTo(escrowId.toString())
    if (expectedVersion != null) {
        assertThat(result.parsed.version).isEqualTo(expectedVersion)
    }
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
    if (expectedVersion != null) {
        assertThat(settleEvent.attributes.firstOrNull { it.key == "version" }?.value)
            .isEqualTo(expectedVersion)
    }

    logSection("Verifying escrow settled")
    val escrow = node.queryDevshardEscrow(escrowId)
    assertThat(escrow.escrow!!.settled).isTrue()

    logSection("Verifying user got refund")
    val balanceAfter = getBalance(user.address)
    assertThat(balanceAfter).isEqualTo(user.fundAmount - totalPayout)

    return result
}

fun LocalInferencePair.findChallengedDevshardInference(
    handle: LocalInferencePair.DevshardProxyHandle,
    numInferences: Long,
): DevshardInferencePayload? {
    // Some flows expose inference IDs starting at 0, others are observed as 1-based.
    // Scan the full inclusive range and ignore missing IDs so the test checks the
    // challenged outcome rather than the local indexing scheme.
    return (0..numInferences).firstNotNullOfOrNull { inferenceId ->
        runCatching {
            cosmosJson.fromJson(
                getDevshardInferenceState(handle.proxyUrl, inferenceId),
                DevshardInferencePayload::class.java,
            )
        }.getOrNull()?.takeIf { it.status == DevshardInferenceStatus.CHALLENGED }
    }
}
