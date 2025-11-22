package com.productscience

import com.productscience.data.Participant
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.tinylog.kotlin.Logger

/**
 * Test utility functions that require testing libraries like assertj.
 * These functions are placed in the test directory to access testing dependencies.
 */

/**
 * Verify that settled inferences result in correct balance changes for all participants.
 * Handles both legacy and Bitcoin reward systems automatically.
 */
fun verifySettledInferences(
    highestFunded: LocalInferencePair,
    inferences: Sequence<InferenceResult>,
    beforeParticipants: List<Participant>,
    startLastRewardedEpoch: Long
) {
    // More than just debugging, this forces the evaluation of the sequence
    val allInferences = inferences.toList()
    logSection("Waiting for settlement and claims")
    highestFunded.waitForStage(EpochStage.START_OF_POC)
    highestFunded.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

    logSection("Verifying balance changes")
    val afterSettleParticipants = highestFunded.api.getParticipants()
    afterSettleParticipants.forEach {
        Logger.info("Participant: ${it.id}, Balance: ${it.balance}")
    }
    val afterSettleInferences = allInferences.map { highestFunded.api.getInference(it.inference.inferenceId) }
    val params = highestFunded.node.getInferenceParams().params

    // Calculate epochs since genesis for Bitcoin reward decay
    val endLastRewardedEpoch = getRewardCalculationEpochIndex(highestFunded)

    val payouts = calculateBalanceChanges(
        afterSettleInferences,
        params,
        beforeParticipants,
        startLastRewardedEpoch,
        endLastRewardedEpoch
    )
    val actualChanges = beforeParticipants.associate {
        it.id to afterSettleParticipants.first { participant -> participant.id == it.id }.balance - it.balance
    }

    actualChanges.forEach { (address, change) ->
        Logger.info("BalanceChange- Participant: $address, Change: $change")
    }

    payouts.forEach { (address, change) ->
        assertThat(actualChanges[address]).`as` { "Participant $address settle change" }
            .isCloseTo(change, Offset.offset(3))
        Logger.info("Participant: $address, Settle Change: $change")
    }
}

/**
 * Helper function to get the proper epoch index for reward calculations.
 * Returns current epoch if CLAIM_REWARDS has happened, otherwise returns previous epoch.
 * This accounts for the timing of when rewards are actually claimed to participant balances.
 */
fun getRewardCalculationEpochIndex(inferencePair: LocalInferencePair): Long {
    val currentBlockHeight = inferencePair.getCurrentBlockHeight() // Live current block height
    val epochResponse = inferencePair.getEpochData()
    val currentEpochIndex = epochResponse.latestEpoch.index
    val claimRewardsBlockHeight = epochResponse.epochStages.claimMoney

    val claimRewardsHappened = currentBlockHeight > claimRewardsBlockHeight
    val rewardCalculationEpoch = if (claimRewardsHappened) {
        currentEpochIndex - 1 // CLAIM_REWARDS has completed, include current epoch
    } else {
        maxOf(currentEpochIndex - 2, 0L) // CLAIM_REWARDS hasn't completed, use previous epoch
    }

    Logger.info("Reward epoch calculation - LiveCurrentBlock: $currentBlockHeight, ClaimRewardsBlock: $claimRewardsBlockHeight")
    Logger.info("Reward epoch result - CurrentEpoch: $currentEpochIndex, ClaimRewardsHappened: $claimRewardsHappened, RewardCalculationEpoch: $rewardCalculationEpoch")

    return rewardCalculationEpoch
}

/**
 * Calculate expected balance change from epoch rewards.
 * Handles both legacy (simple refund) and Bitcoin (refund + cumulative epoch rewards) systems.
 *
 * @param inferencePair LocalInferencePair to get parameters and participant information
 * @param participantAddress Address of the participant to calculate changes for
 * @param startEpochIndex Epoch index when test measurement started
 * @param currentEpochIndex Current epoch index to use for reward calculations (should come from getRewardCalculationEpochIndex)
 * @param failureEpoch Epoch where failure occurred to exclude from rewards (default: null for no failure)
 * @return Expected balance change (refund + cumulative epoch rewards for Bitcoin system, 0 for legacy)
 */
fun calculateExpectedChangeFromEpochRewards(
    inferencePair: LocalInferencePair,
    participantAddress: String,
    startEpochIndex: Long,
    currentEpochIndex: Long,
    failureEpoch: Long? = null
): Long {
    val params = inferencePair.node.getInferenceParams().params
    val participants = inferencePair.api.getParticipants()

    // Determine which epochs to exclude from rewards
    val excludedEpochs = if (failureEpoch != null) {
        setOf(failureEpoch)
    } else {
        emptySet()
    }

    Logger.info("Bitcoin reward calculation - Participant: $participantAddress")
    Logger.info("Bitcoin reward params - Use: ${params.bitcoinRewardParams?.useBitcoinRewards}, Initial: ${params.bitcoinRewardParams?.initialEpochReward}, Decay: ${params.bitcoinRewardParams?.decayRate}")
    Logger.info("Epoch timing - StartLastRewardedEpoch: $startEpochIndex, CurrentLastRewardedEpoch: $currentEpochIndex")
    Logger.info("Excluded epochs: $excludedEpochs, Participants count: ${participants.size}")

    // calculateCumulativeEpochRewards handles all Bitcoin/Legacy checks internally
    val reward = calculateCumulativeEpochRewards(
        participants,
        params.bitcoinRewardParams,
        startEpochIndex,
        currentEpochIndex,
        excludedEpochs
    )[participantAddress] ?: 0L

    Logger.info("Final calculated reward for $participantAddress: $reward")
    return reward
} 
