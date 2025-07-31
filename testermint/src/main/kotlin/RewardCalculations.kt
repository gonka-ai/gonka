package com.productscience

import com.productscience.data.BitcoinRewardParams
import com.productscience.data.InferenceParams
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceStatus
import com.productscience.data.Participant
import org.tinylog.kotlin.Logger

/**
 * Common utility functions for reward calculations in tests.
 * Supports both legacy (immediate bonus) and Bitcoin (epoch settlement) reward systems.
 */

/**
 * Calculate epochs since genesis for Bitcoin reward decay calculation.
 * Rewards are distributed for the previous epoch's work, so we subtract 1 from current epoch.
 */
fun getEpochsSinceGenesis(inferencePair: LocalInferencePair, params: InferenceParams): Long {
    val currentEpochIndex = inferencePair.getEpochData().latestEpoch.index
    val genesisEpoch = params.bitcoinRewardParams?.genesisEpoch ?: 1L
    val workEpoch = currentEpochIndex - 1 // Rewards distributed for previous epoch's work
    return maxOf(workEpoch - genesisEpoch, 0L) // Ensure non-negative
}

/**
 * Calculate cumulative Bitcoin epoch rewards across a range of epochs.
 * This handles scenarios where participants earn rewards over multiple epochs before failing.
 * Automatically checks if Bitcoin rewards are enabled and returns empty map if not.
 * 
 * @param participants List of participants eligible for rewards
 * @param bitcoinParams Bitcoin reward parameters (nullable - if null or Bitcoin disabled, returns empty)
 * @param startEpoch Starting epoch for reward calculation (inclusive)
 * @param endEpoch Ending epoch for reward calculation (inclusive) 
 * @param excludeEpochs Set of epochs to exclude (e.g., epochs where participant was INVALID)
 * @return Map of participant ID to cumulative reward amount (empty if Bitcoin rewards disabled)
 */
fun calculateCumulativeEpochRewards(
    participants: List<Participant>,
    bitcoinParams: BitcoinRewardParams?,
    startEpoch: Long,
    endEpoch: Long,
    excludeEpochs: Set<Long> = emptySet()
): Map<String, Long> {
    // Return empty map if Bitcoin rewards are not enabled or no participants
    if (!isBitcoinRewardsEnabled(bitcoinParams) || participants.isEmpty() || endEpoch <= startEpoch) {
        return emptyMap()
    }
    val genesisEpoch = bitcoinParams!!.genesisEpoch
    val initialReward = bitcoinParams.initialEpochReward
    val decayRate = bitcoinParams.decayRate.toDouble()
    
    val cumulativeRewards = mutableMapOf<String, Long>()

    val firstEpoch = maxOf(startEpoch + 1, 0L)
    
    // Calculate rewards for each epoch in the range
    for (epoch in firstEpoch..endEpoch) {
        if (epoch in excludeEpochs) {
            Logger.info("Skipping epoch $epoch (excluded - participant was INVALID)")
            continue
        }
        
        // Calculate reward for this specific epoch
        val epochsSinceGenesis = epoch - genesisEpoch
        val epochReward = (initialReward * kotlin.math.exp(decayRate * epochsSinceGenesis)).toLong()
        
        // Distribute proportionally among participants (assume equal PoC weight for E2E testing)
        val totalPocWeight = participants.sumOf { 100L } // Assume 100 PoC weight per participant
        var totalDistributed = 0L
        val epochRewards = mutableMapOf<String, Long>()
        
        participants.forEach { participant ->
            val participantPocWeight = 100L // Assume equal PoC weight for testing
            val participantReward = (epochReward * participantPocWeight) / totalPocWeight
            epochRewards[participant.id] = participantReward
            totalDistributed += participantReward
        }
        
        // Distribute any remainder due to integer division truncation (same as bitcoin_rewards.go)
        val remainder = epochReward - totalDistributed
        if (remainder > 0 && participants.isNotEmpty()) {
            // Assign remainder to first participant (matches blockchain logic)
            val firstParticipant = participants.first()
            epochRewards[firstParticipant.id] = (epochRewards[firstParticipant.id] ?: 0L) + remainder
            Logger.info("Epoch $epoch - Distributed remainder $remainder to first participant: ${firstParticipant.id}")
        }
        
        // Add epoch rewards to cumulative totals
        epochRewards.forEach { (participantId, reward) ->
            cumulativeRewards[participantId] = (cumulativeRewards[participantId] ?: 0L) + reward
            Logger.info("Epoch $epoch - Participant: $participantId, Reward: $reward, Cumulative: ${cumulativeRewards[participantId]}")
        }
        
        Logger.info("Epoch $epoch - Total Reward: $epochReward, Participants: ${participants.size}")
    }
    
    return cumulativeRewards
}

/**
 * Check if Bitcoin rewards are enabled by reading the governance parameter
 */
fun isBitcoinRewardsEnabled(bitcoinParams: BitcoinRewardParams?): Boolean {
    return bitcoinParams?.useBitcoinRewards ?: false
}

/**
 * Calculate Bitcoin epoch settlement rewards distributed to all participants based on PoC weight
 * This simulates the epoch settlement process for E2E testing
 */
fun calculateBitcoinEpochRewards(
    participants: List<Participant>, 
    bitcoinParams: BitcoinRewardParams,
    epochsSinceGenesis: Long
): Map<String, Long> {
    val initialReward = bitcoinParams.initialEpochReward
    val decayRate = bitcoinParams.decayRate.toDouble() // Convert Decimal to Double
    val genesisEpoch = bitcoinParams.genesisEpoch
    
    // Calculate proper decay based on actual epochs elapsed
    val epochsForDecay = epochsSinceGenesis.toDouble()
    val currentEpochReward = (initialReward * kotlin.math.exp(decayRate * epochsForDecay)).toLong()
    
    // Calculate total PoC weight (simulate from participant count for E2E testing)
    val totalPocWeight = participants.sumOf { 100L } // Assume 100 PoC weight per participant for testing
    
    val rewards = mutableMapOf<String, Long>()
    var totalDistributed = 0L
    
    participants.forEach { participant ->
        val participantPocWeight = 100L // Assume equal PoC weight for E2E testing
        val participantReward = (currentEpochReward * participantPocWeight) / totalPocWeight
        rewards[participant.id] = participantReward
        totalDistributed += participantReward
        Logger.info("Bitcoin Epoch Settlement - Participant: ${participant.id}, PoC Weight: $participantPocWeight, Reward: $participantReward")
    }
    
    // Distribute any remainder due to integer division truncation (same as bitcoin_rewards.go)
    val remainder = currentEpochReward - totalDistributed
    if (remainder > 0 && participants.isNotEmpty()) {
        // Assign remainder to first participant (matches blockchain logic)
        val firstParticipant = participants.first()
        rewards[firstParticipant.id] = (rewards[firstParticipant.id] ?: 0L) + remainder
        Logger.info("Bitcoin Epoch Settlement - Distributed remainder $remainder to first participant: ${firstParticipant.id}")
    }
    
    Logger.info("Bitcoin Epoch Settlement - Epochs Since Genesis: $epochsSinceGenesis, Current Epoch Reward: $currentEpochReward, Total Participants: ${participants.size}")
    return rewards
}

/**
 * Calculate immediate reward bonuses for inference work.
 * Returns 0 for Bitcoin system (rewards come from epoch settlement).
 * Returns calculated bonus for legacy system (immediate bonuses).
 */
fun calculateRewards(params: InferenceParams, earned: Long, bitcoinParams: BitcoinRewardParams?): Long {
    if (isBitcoinRewardsEnabled(bitcoinParams)) {
        // Bitcoin system: No immediate bonuses - rewards come from epoch settlement
        Logger.info(
            "Bitcoin System - Owed: $earned, Immediate RewardCoins: 0 (rewards distributed at epoch settlement)"
        )
        return 0
    }
    // Legacy system: Immediate bonuses based on subsidy percentage
    val bonusPercentage = params.tokenomicsParams.currentSubsidyPercentage
    val coinsForParticipant = (earned / (1 - bonusPercentage.toDouble())).toLong()
    Logger.info(
        "Legacy System - Owed: $earned, Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant"
    )
    return coinsForParticipant
}

/**
 * Calculate expected balance changes for all participants after inference settlement.
 * Handles both legacy (immediate bonuses) and Bitcoin (epoch settlement) reward systems.
 * 
 * @param inferences List of settled inference payloads
 * @param inferenceParams System parameters to determine reward system
 * @param participants List of participants for Bitcoin epoch settlement calculation
 * @param startLastRewardedEpoch The last epoch that was already rewarded when measurement started
 * @param endLastRewardedEpoch The current last rewarded epoch (Bitcoin rewards calculated from startLastRewardedEpoch + 1 to endLastRewardedEpoch)
 * @return Map of participant ID to expected balance change
 */
fun calculateBalanceChanges(
    inferences: List<InferencePayload>,
    inferenceParams: InferenceParams,
    participants: List<Participant> = emptyList(),
    startLastRewardedEpoch: Long = 0L,
    endLastRewardedEpoch: Long = 0L,
): Map<String, Long> {
    val payouts: MutableMap<String, Long> = mutableMapOf()
    inferences.forEach { inference ->
        when (inference.status) {
            InferenceStatus.STARTED.value -> {
                require(inference.escrowAmount != null) { "Escrow amount is null for started inference" }
                payouts.add(inference.requestedBy!!, inference.escrowAmount!!, "initial escrow")
            }
            // no payouts
            InferenceStatus.FINISHED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for finished inference" }
                // refund from escrow
                payouts.add(inference.requestedBy!!, -inference.actualCost!!, "actual cost")
                payouts.add(inference.assignedTo!!, inference.actualCost!!, "full inference")
                payouts.add(
                    inference.assignedTo!!,
                    calculateRewards(inferenceParams, inference.actualCost!!, inferenceParams.bitcoinRewardParams),
                    "reward for inference"
                )
            }

            InferenceStatus.VALIDATED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                require(inference.validatedBy.isNotEmpty()) { "Validated by is empty for validated inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for validated inference" }
                val allValidators = listOf(inference.assignedTo) + inference.validatedBy
                val workCoins = allValidators.associateWith { validator ->
                    if (validator == inference.assignedTo) {
                        payouts.add(
                            key = validator!!,
                            amount = inference.actualCost!! / allValidators.size,
                            reason = "Validation distributed work"
                        )
                        payouts.add(
                            key = validator,
                            amount = inference.actualCost!! % allValidators.size,
                            reason = "Validation distribution remainder"
                        )
                        inference.actualCost!! / allValidators.size + inference.actualCost!! % allValidators.size
                    } else {
                        payouts.add(
                            key = validator!!,
                            amount = inference.actualCost!! / allValidators.size,
                            reason = "Validation distributed work"
                        )
                        inference.actualCost!! / allValidators.size
                    }
                }
                workCoins.forEach { (validator, cost) ->
                    payouts.add(validator!!, calculateRewards(inferenceParams, cost, inferenceParams.bitcoinRewardParams), "reward for work")
                }

                // refund from escrow
                payouts.add(inference.requestedBy!!, -inference.actualCost!!, "actual cost")
            }

            InferenceStatus.EXPIRED.value, InferenceStatus.INVALIDATED.value -> {
                // full refund
                payouts.add(inference.requestedBy!!, 0, "full refund of expired or invalidated")
            }
        }
    }
    
    // Add Bitcoin epoch settlement rewards (function handles Bitcoin system check internally)
    val bitcoinCumulativeRewards = calculateCumulativeEpochRewards(
        participants,
        inferenceParams.bitcoinRewardParams,
        startLastRewardedEpoch,
        endLastRewardedEpoch,
        emptySet() // No excluded epochs for normal settlement
    )
    bitcoinCumulativeRewards.forEach { (participantId, reward) ->
        payouts.add(participantId, reward, "Bitcoin cumulative epoch settlement rewards (epochs ${startLastRewardedEpoch + 1} to $endLastRewardedEpoch)")
    }
    
    return payouts
}

/**
 * Helper extension function to add amounts to payout map with logging
 */
fun MutableMap<String, Long>.add(key: String, amount: Long, reason: String) {
    Logger.info("$key:$amount for $reason")
    this[key] = (this[key] ?: 0) + amount
}

/**
 * Calculate expected immediate balance changes from inferences (WorkCoins only).
 * This function only handles immediate WorkCoins distribution and does not include RewardCoins.
 * For complete balance calculation including RewardCoins, use calculateBalanceChanges() instead.
 */
fun expectedCoinBalanceChanges(inferences: List<InferencePayload>): Map<String, Long> {
    val payouts: MutableMap<String, Long> = mutableMapOf()
    inferences.forEach { inference ->
        when (inference.status) {
            InferenceStatus.STARTED.value -> {}
            // no payouts
            InferenceStatus.FINISHED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                payouts.add(inference.assignedTo!!, inference.actualCost!!, "Full Inference (WorkCoins)")
            }

            InferenceStatus.VALIDATED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                val validators = listOf(inference.assignedTo) + inference.validatedBy
                validators.forEach { validator ->
                    payouts.add(validator!!, inference.actualCost!! / validators.size, "Validator share (WorkCoins)")
                }
                payouts.add(inference.assignedTo!!, inference.actualCost!! % validators.size, "Validator remainder (WorkCoins)")
            }
        }
    }
    return payouts
}

/**
 * Calculate expected vesting schedule changes for all participants.
 * Separates immediate costs (negative, epoch 0) from vested earnings (positive, distributed over epochs).
 * Handles WorkCoins and RewardCoins separately with their own vesting periods and remainder distribution.
 * 
 * @param inferences List of settled inference payloads
 * @param inferenceParams System parameters to determine reward system and vesting periods
 * @param participants List of participants for Bitcoin epoch settlement calculation
 * @param startLastRewardedEpoch Starting epoch index for Bitcoin reward calculations
 * @param endLastRewardedEpoch Ending epoch index for Bitcoin reward calculations
 * @param vestingPeriod Number of epochs over which positive earnings should vest (defaults to rewardVestingPeriod)
 * @return Map of participant address to array of balance changes per epoch [immediate_costs, vested_epoch_1, vested_epoch_2, ...]
 */
fun calculateVestingScheduleChanges(
    inferences: List<InferencePayload>,
    inferenceParams: InferenceParams,
    participants: List<Participant> = emptyList(),
    startLastRewardedEpoch: Long = 0L,
    endLastRewardedEpoch: Long = 0L,
    vestingPeriod: Int = -1 // -1 means use rewardVestingPeriod from params
): Map<String, LongArray> {
    
    // Use parameter vesting periods if not explicitly provided
    val workVestingPeriod = (inferenceParams.tokenomicsParams.workVestingPeriod ?: 0L).toInt()
    val rewardVestingPeriod = if (vestingPeriod == -1) (inferenceParams.tokenomicsParams.rewardVestingPeriod ?: 0L).toInt() else vestingPeriod
    val maxVestingPeriod = maxOf(workVestingPeriod, rewardVestingPeriod)
    
    // Track WorkCoins and RewardCoins separately for each participant
    val workCoins: MutableMap<String, Long> = mutableMapOf()
    val rewardCoins: MutableMap<String, Long> = mutableMapOf()
    val costs: MutableMap<String, Long> = mutableMapOf()
    
    // Process each inference following the same logic as calculateBalanceChanges
    inferences.forEach { inference ->
        when (inference.status) {
            InferenceStatus.STARTED.value -> {
                require(inference.escrowAmount != null) { "Escrow amount is null for started inference" }
                costs.add(inference.requestedBy!!, inference.escrowAmount!!)
            }
            
            InferenceStatus.FINISHED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for finished inference" }
                require(inference.assignedTo != null) { "Assigned to is null for finished inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for finished inference" }
                
                // Consumer pays actual cost
                costs.add(inference.requestedBy!!, inference.actualCost!!)
                
                // Executor gets WorkCoins (actual cost) and RewardCoins (bonus)
                workCoins.add(inference.assignedTo!!, inference.actualCost!!)
                val rewardAmount = calculateRewards(inferenceParams, inference.actualCost!!, inferenceParams.bitcoinRewardParams)
                rewardCoins.add(inference.assignedTo!!, rewardAmount)
            }

            InferenceStatus.VALIDATED.value -> {
                require(inference.actualCost != null) { "Actual cost is null for validated inference" }
                require(inference.assignedTo != null) { "Assigned to is null for validated inference" }
                require(inference.validatedBy.isNotEmpty()) { "Validated by is empty for validated inference" }
                require(inference.escrowAmount != null) { "Escrow amount is null for validated inference" }
                
                val allValidators = listOf(inference.assignedTo) + inference.validatedBy
                
                // Consumer pays actual cost
                costs.add(inference.requestedBy!!, inference.actualCost!!)
                
                // Distribute WorkCoins and RewardCoins among validators
                allValidators.forEachIndexed { index, validator ->
                    val workCoinAmount = if (index == 0) { // First validator (assignedTo) gets remainder
                        inference.actualCost!! / allValidators.size + inference.actualCost!! % allValidators.size
                    } else {
                        inference.actualCost!! / allValidators.size
                    }
                    
                    workCoins.add(validator!!, workCoinAmount)
                    val rewardAmount = calculateRewards(inferenceParams, workCoinAmount, inferenceParams.bitcoinRewardParams)
                    rewardCoins.add(validator, rewardAmount)
                }
            }

            InferenceStatus.EXPIRED.value, InferenceStatus.INVALIDATED.value -> {
                // No costs or rewards for expired/invalidated inferences
            }
        }
    }
    
    // Add Bitcoin epoch settlement rewards (function handles Bitcoin system check internally)
    val bitcoinCumulativeRewards = calculateCumulativeEpochRewards(
        participants,
        inferenceParams.bitcoinRewardParams,
        startLastRewardedEpoch,
        endLastRewardedEpoch,
        emptySet()
    )
    bitcoinCumulativeRewards.forEach { (participantId, reward) ->
        rewardCoins.add(participantId, reward)
    }
    
    // Create vesting schedules for each participant
    val vestingSchedules = mutableMapOf<String, LongArray>()
    val allParticipants = (costs.keys + workCoins.keys + rewardCoins.keys).toSet()
    
    allParticipants.forEach { participantAddress ->
        val participantCosts = costs[participantAddress] ?: 0L
        val participantWorkCoins = workCoins[participantAddress] ?: 0L
        val participantRewardCoins = rewardCoins[participantAddress] ?: 0L
        
        // Create schedule: [immediate_costs, vested_epoch_1, vested_epoch_2, ...]
        val schedule = LongArray(maxVestingPeriod + 1) { 0L }
        
        // Epoch 0: Immediate costs (negative)
        schedule[0] = -participantCosts
        
        // Vest WorkCoins over workVestingPeriod
        if (participantWorkCoins > 0 && workVestingPeriod > 0) {
            val workPerEpoch = participantWorkCoins / workVestingPeriod
            val workRemainder = participantWorkCoins % workVestingPeriod
            
            for (epoch in 1..workVestingPeriod) {
                schedule[epoch] += workPerEpoch
                if (epoch == 1) { // Add remainder to first epoch
                    schedule[epoch] += workRemainder
                }
            }
        }
        
        // Vest RewardCoins over rewardVestingPeriod
        if (participantRewardCoins > 0 && rewardVestingPeriod > 0) {
            val rewardPerEpoch = participantRewardCoins / rewardVestingPeriod
            val rewardRemainder = participantRewardCoins % rewardVestingPeriod
            
            for (epoch in 1..rewardVestingPeriod) {
                schedule[epoch] += rewardPerEpoch
                if (epoch == 1) { // Add remainder to first epoch
                    schedule[epoch] += rewardRemainder
                }
            }
        }
        
        vestingSchedules[participantAddress] = schedule
    }
    
    return vestingSchedules
}

/**
 * Helper extension function to add amounts to mutable map
 */
private fun MutableMap<String, Long>.add(key: String, amount: Long) {
    this[key] = (this[key] ?: 0) + amount
} 