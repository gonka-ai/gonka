import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.LocalInferencePair
import com.productscience.data.*
import com.productscience.initCluster
import com.productscience.logSection
import org.tinylog.kotlin.Logger
import kotlin.math.floor

fun createPoCChallengeSpec(
    expectedConfirmationsPerEpoch: Long,
    pocStageDuration: Long = 10L,
    alphaThreshold: Double = 0.70,
    minPunishableSegmentBlocks: Long = 8L,
): Spec<AppState> {
    return spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::epochLength] = 40L
                    this[EpochParams::pocStageDuration] = pocStageDuration
                    this[EpochParams::pocValidationDuration] = 4L
                    this[EpochParams::pocExchangeDuration] = 2L
                    this[EpochParams::pocValidationDelay] = 1L
                    this[EpochParams::setNewValidatorsDelay] = 1L
                    this[EpochParams::confirmationPocSafetyWindow] = 0L
                }
                this[InferenceParams::confirmationPocParams] = spec<ConfirmationPoCParams> {
                    this[ConfirmationPoCParams::expectedConfirmationsPerEpoch] = expectedConfirmationsPerEpoch
                    this[ConfirmationPoCParams::alphaThreshold] = Decimal.fromDouble(alphaThreshold)
                    this[ConfirmationPoCParams::slashFraction] = Decimal.fromDouble(0.10)
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocDataPruningEpochThreshold] = 10L
                    this[PocParams::pocV2Enabled] = true
                }
                this[InferenceParams::pocChallengeParams] = spec<PocChallengeParams> {
                    this[PocChallengeParams::minPunishableSegmentBlocks] = minPunishableSegmentBlocks
                    this[PocChallengeParams::maxActiveChallenges] = 4L
                    this[PocChallengeParams::paymentRatio] = Decimal.fromDouble(0.1)
                }
            }
        }
    }
}

data class PoCChallengeCluster(
    val cluster: LocalCluster,
    val genesis: LocalInferencePair,
    val join1: LocalInferencePair,
    val join2: LocalInferencePair,
)

fun bootPoCChallengeCluster(
    expectedConfirmationsPerEpoch: Long,
    pocStageDuration: Long,
    alphaThreshold: Double,
): PoCChallengeCluster {
    val spec = createPoCChallengeSpec(
        expectedConfirmationsPerEpoch = expectedConfirmationsPerEpoch,
        pocStageDuration = pocStageDuration,
        alphaThreshold = alphaThreshold,
    )
    val (cluster, genesis) = initCluster(
        joinCount = 2,
        mergeSpec = spec,
        reboot = true,
        resetMlNodes = false,
    )
    val join1 = cluster.joinPairs[0]
    val join2 = cluster.joinPairs[1]

    logSection("Waiting for first START_OF_POC and epoch 1 inference")
    genesis.waitForStage(EpochStage.START_OF_POC)
    genesis.waitForStage(EpochStage.CLAIM_REWARDS)
    waitUntilInference(genesis)

    logSection("Allowing genesis as challenger during epoch 1")
    allowChallenger(cluster, genesis)

    logSection("Waiting for epoch 2 inference")
    genesis.waitForStage(EpochStage.START_OF_POC)
    genesis.waitForStage(EpochStage.CLAIM_REWARDS)
    waitUntilInference(genesis)

    return PoCChallengeCluster(cluster, genesis, join1, join2)
}

fun allowChallenger(cluster: LocalCluster, genesis: LocalInferencePair) {
    val params = genesis.getParams()
    val current = params.devshardEscrowParams
        ?: error("devshard escrow params missing")
    genesis.runProposal(
        cluster,
        UpdateParams(
            params = params.copy(
                devshardEscrowParams = current.copy(
                    allowedCreatorAddresses = listOf(genesis.node.getColdAddress()),
                ),
            ),
        ),
    )
}

fun enableConfirmationPoc(
    cluster: LocalCluster,
    genesis: LocalInferencePair,
    expectedConfirmationsPerEpoch: Long,
) {
    val params = genesis.getParams()
    val current = params.confirmationPocParams ?: ConfirmationPoCParams()
    genesis.runProposal(
        cluster,
        UpdateParams(
            params = params.copy(
                confirmationPocParams = current.copy(
                    expectedConfirmationsPerEpoch = expectedConfirmationsPerEpoch,
                ),
            ),
        ),
    )
}

fun waitUntilInference(pair: LocalInferencePair) {
    while (pair.getEpochData().phase != EpochPhase.Inference) {
        pair.node.waitForMinimumBlock(pair.getCurrentBlockHeight() + 1, "inference")
    }
}

const val ChallengeCommitLeadBlocks = 4L
const val DoubleWindowDurationMin = 11L
const val DoubleWindowDurationMax = 14L

fun lastSegmentCreateHeight(pair: LocalInferencePair): Long {
    val epoch = pair.getEpochData()
    val params = pair.getParams().epochParams
    return epoch.epochStages.nextPocStart - params.pocStageDuration - params.pocExchangeDuration - 1
}

fun doubleWindowCreateHeight(pair: LocalInferencePair): Long {
    val epoch = pair.getEpochData()
    val params = pair.getParams().epochParams
    return epoch.epochStages.nextPocStart - 2 * (params.pocStageDuration + params.pocExchangeDuration) - 1
}

fun createAtLastSegment(genesis: LocalInferencePair, target: String): TxResponse {
    val height = lastSegmentCreateHeight(genesis)
    val now = genesis.getCurrentBlockHeight()
    require(now <= height) { "already past last-segment create height $height (now $now)" }
    genesis.node.waitForMinimumBlock(height, "last-segment create")
    val epoch = genesis.getEpochData()
    require(epoch.phase == EpochPhase.Inference) { "last-segment create is not in inference: ${epoch.phase}" }
    require(!epoch.isConfirmationPocActive) { "cPoC is active at last-segment create" }
    val resp = genesis.createPoCChallenge(target)
    require(resp.code == 0) { "create-poc-challenge failed: ${resp.rawLog}" }
    return resp
}

fun createAtDoubleWindow(genesis: LocalInferencePair, target: String): OpenPoCChallenge {
    val height = doubleWindowCreateHeight(genesis)
    val now = genesis.getCurrentBlockHeight()
    require(now <= height) { "already past double-window create height $height (now $now)" }
    genesis.node.waitForMinimumBlock(height, "double-window create")
    val epoch = genesis.getEpochData()
    require(epoch.phase == EpochPhase.Inference) { "double-window create is not in inference: ${epoch.phase}" }
    require(!epoch.isConfirmationPocActive) { "cPoC is active at double-window create" }
    val resp = genesis.createPoCChallenge(target)
    require(resp.code == 0) { "create-poc-challenge failed: ${resp.rawLog}" }
    val open = waitForOpenChallenge(genesis, target)
    requireLandedDoubleWindow(open)
    return open
}

fun requireLandedDoubleWindow(ch: OpenPoCChallenge) {
    val duration = ch.finish - ch.startHeight
    require(duration in DoubleWindowDurationMin..DoubleWindowDurationMax) {
        "landed duration $duration not in $DoubleWindowDurationMin..$DoubleWindowDurationMax start=${ch.startHeight} finish=${ch.finish}"
    }
    require(ch.generating) { "challenge not generating after double-window create" }
}

fun requireEmitWindow(ch: OpenPoCChallenge, now: Long, lead: Long = ChallengeCommitLeadBlocks) {
    require(now <= ch.finish - lead - 2) {
        "too late to emit extra batch: now=$now finish=${ch.finish} lead=$lead"
    }
}

fun createChallengeFirst(genesis: LocalInferencePair, target: String): TxResponse {
    while (true) {
        val epoch = genesis.getEpochData()
        val remaining = epoch.epochStages.nextPocStart - (epoch.blockHeight + 1)
        if (remaining < 8) {
            error("remaining-to-safety $remaining dropped below 8 before create")
        }
        if (epoch.isConfirmationPocActive) {
            error("cPoC became active before create")
        }
        if (epoch.phase != EpochPhase.Inference) {
            genesis.node.waitForMinimumBlock(epoch.blockHeight + 1, "wait inference for create")
            continue
        }
        val resp = genesis.createPoCChallenge(target)
        if (resp.code == 0) {
            return resp
        }
        Logger.warn("create-poc-challenge failed at height ${epoch.blockHeight}: ${resp.rawLog}")
        genesis.node.waitForMinimumBlock(epoch.blockHeight + 1, "retry create")
    }
}

fun challengeOf(pair: LocalInferencePair, target: String, height: Long? = null): OpenPoCChallenge? {
    return pair.node.queryOpenPoCChallenges(height).challenges.firstOrNull { it.target == target }
}

fun waitForOpenChallenge(pair: LocalInferencePair, target: String, maxBlocks: Int = 20): OpenPoCChallenge {
    repeat(maxBlocks) {
        challengeOf(pair, target)?.let { return it }
        pair.node.waitForMinimumBlock(pair.getCurrentBlockHeight() + 1, "open challenge")
    }
    error("no open challenge for $target")
}

fun waitForChallengeCommit(pair: LocalInferencePair, target: String, maxBlocks: Int = 30): OpenPoCChallenge {
    repeat(maxBlocks) {
        val ch = challengeOf(pair, target)
        if (ch != null && ch.commits.isNotEmpty()) {
            return ch
        }
        pair.node.waitForMinimumBlock(pair.getCurrentBlockHeight() + 1, "challenge commit")
    }
    error("no challenge commit for $target")
}

fun waitForChallengeCommitCount(
    pair: LocalInferencePair,
    target: String,
    count: Long,
    startHeight: Long,
    deadline: Long,
): OpenPoCChallenge {
    while (true) {
        val height = pair.getCurrentBlockHeight()
        val ch = challengeOf(pair, target)
        val matched = ch != null &&
            ch.startHeight == startHeight &&
            ch.commits.any { commit ->
                commit.count >= count &&
                    (commit.pocStageStartBlockHeight == 0L || commit.pocStageStartBlockHeight == startHeight)
            }
        if (matched) {
            return ch!!
        }
        if (height >= deadline) {
            error(
                "no commit count $count for $target start=$startHeight by deadline $deadline " +
                    "(height=$height last=${ch?.commits})"
            )
        }
        pair.node.waitForMinimumBlock(height + 1, "challenge commit count $count")
    }
}

fun waitForRotatedCommit(
    pair: LocalInferencePair,
    target: String,
    startHeight: Long,
    maxBlocks: Int = 30,
): OpenPoCChallenge {
    repeat(maxBlocks) {
        val ch = challengeOf(pair, target)
        if (ch != null && ch.commits.any { it.pocStageStartBlockHeight == startHeight && it.count > 0 }) {
            return ch
        }
        pair.node.waitForMinimumBlock(pair.getCurrentBlockHeight() + 1, "rotated commit")
    }
    error("no commit for rotated start $startHeight")
}

data class ChallengeSnapshot(
    val challenge: OpenPoCChallenge,
    val height: Long,
)

fun snapshotChallengeAtEndOfPoCValidation(pair: LocalInferencePair, target: String): ChallengeSnapshot {
    val end = pair.getNextStage(EpochStage.END_OF_POC_VALIDATION)
    pair.node.waitForMinimumBlock(end, "endOfPoCValidation")
    val challenge = challengeOf(pair, target, height = end)
        ?: error("challenge gone at end of poc validation height $end")
    return ChallengeSnapshot(challenge, end)
}

fun participantAtHeight(pair: LocalInferencePair, target: LocalInferencePair, height: Long): RawParticipant? {
    return pair.node.getRawParticipants(height).getParticipant(target)
}

fun confirmationWeight(pair: LocalInferencePair, epoch: Long, addr: String): Long {
    val group = pair.node.queryEpochGroupData(epoch).epochGroupData
    return group.validationWeights.firstOrNull { it.memberAddress == addr }?.confirmationWeight
        ?: error("no validation weight for $addr in epoch $epoch")
}

fun requireLandedPunishable(ch: OpenPoCChallenge, minPunishable: Long = 8) {
    val duration = ch.finish - ch.startHeight
    require(duration >= minPunishable) { "landed duration $duration < $minPunishable" }
    require(ch.generating) { "challenge not generating after create" }
}

fun expectedScaledWeight(mockWeight: Long, duration: Long, stagePlusExchange: Long): Long {
    if (duration <= 0) return 0
    return floor(mockWeight * stagePlusExchange.toDouble() / duration.toDouble()).toLong()
        .coerceAtMost(mockWeight)
}

fun expectedNormalizedWeight(
    count: Long,
    duration: Long,
    stagePlusExchange: Long,
    epochWeight: Long = 10,
): Long {
    if (duration <= 0) return 0
    return floor(count * stagePlusExchange.toDouble() / duration.toDouble()).toLong()
        .coerceAtMost(epochWeight)
}

fun stagePlusExchange(pair: LocalInferencePair): Long {
    val params = pair.getParams().epochParams
    return params.pocStageDuration + params.pocExchangeDuration
}

fun waitForConfirmationPoCInEpoch(pair: LocalInferencePair, epochIndex: Long): ConfirmationPoCEvent {
    while (true) {
        val epoch = pair.getEpochData()
        if (epoch.latestEpoch.index != epochIndex) {
            error("epoch advanced to ${epoch.latestEpoch.index} before cPoC in epoch $epochIndex")
        }
        val event = epoch.activeConfirmationPocEvent
        if (epoch.isConfirmationPocActive && event != null && event.epochIndex == epochIndex) {
            return event
        }
        val params = pair.getParams().epochParams
        val windowEnd = epoch.epochStages.nextPocStart -
            params.inferenceValidationCutoff -
            params.pocStageDuration -
            params.pocExchangeDuration -
            params.pocValidationDelay -
            params.pocValidationDuration -
            params.setNewValidatorsDelay -
            params.confirmationPocSafetyWindow
        if (epoch.blockHeight > windowEnd) {
            error("cPoC trigger window ended at $windowEnd without an event in epoch $epochIndex")
        }
        pair.node.waitForMinimumBlock(epoch.blockHeight + 1, "cPoC same epoch")
    }
}

fun setAllPocV2Weights(pairs: List<LocalInferencePair>, weights: List<Long>) {
    require(pairs.size == weights.size)
    pairs.zip(weights).forEach { (pair, weight) -> pair.setPocV2Weight(weight) }
}
