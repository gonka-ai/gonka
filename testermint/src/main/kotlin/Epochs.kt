package com.productscience

import com.productscience.data.EpochParams
import com.productscience.data.EpochResponse

enum class EpochStage {
    START_OF_POC,
    END_OF_POC,
    POC_EXCHANGE_DEADLINE,
    START_OF_POC_VALIDATION,
    END_OF_POC_VALIDATION,
    SET_NEW_VALIDATORS,
    CLAIM_REWARDS
}

fun EpochResponse.getNextStage(stage: EpochStage): Long {
    return when (stage) {
        EpochStage.START_OF_POC -> resolveUpcomingStage(epochStages.pocStart, nextEpochStages.pocStart)
        EpochStage.END_OF_POC -> resolveUpcomingStage(epochStages.pocGenerationEnd, nextEpochStages.pocGenerationEnd)
        EpochStage.POC_EXCHANGE_DEADLINE -> resolveUpcomingStage(epochStages.pocExchangeWindow.end, nextEpochStages.pocExchangeWindow.end)
        EpochStage.START_OF_POC_VALIDATION -> resolveUpcomingStage(epochStages.pocValidationStart, nextEpochStages.pocValidationStart)
        EpochStage.END_OF_POC_VALIDATION -> resolveUpcomingStage(epochStages.pocValidationEnd, nextEpochStages.pocValidationEnd)
        EpochStage.SET_NEW_VALIDATORS -> resolveUpcomingStage(epochStages.setNewValidators, nextEpochStages.setNewValidators)
        EpochStage.CLAIM_REWARDS -> resolveUpcomingStage(epochStages.claimMoney, nextEpochStages.claimMoney)
    }
}

fun EpochResponse.resolveUpcomingStage(latestEpochStage: Long, nextEpochStage: Long): Long {
    assert(latestEpochStage < nextEpochStage)
    return if (blockHeight < latestEpochStage) {
        latestEpochStage
    } else {
        nextEpochStage
    }
}

@Deprecated("Use EpochResponse.getNextStage instead. We keep it only to get the block when the very 1st validators are active.")
fun EpochParams.getStage(stage: EpochStage): Long = when (stage) {
    EpochStage.START_OF_POC -> 0L
    EpochStage.END_OF_POC -> getStage(EpochStage.START_OF_POC) + pocValidationDuration * epochMultiplier
    EpochStage.POC_EXCHANGE_DEADLINE -> getStage(EpochStage.END_OF_POC) + pocExchangeDuration * epochMultiplier
    EpochStage.START_OF_POC_VALIDATION -> getStage(EpochStage.END_OF_POC) + pocValidationDelay * epochMultiplier
    EpochStage.END_OF_POC_VALIDATION -> getStage(EpochStage.START_OF_POC_VALIDATION) + pocValidationDuration * epochMultiplier
    EpochStage.SET_NEW_VALIDATORS -> getStage(EpochStage.END_OF_POC_VALIDATION) + 1 * epochMultiplier
    EpochStage.CLAIM_REWARDS -> getStage(EpochStage.SET_NEW_VALIDATORS) + 1 * epochMultiplier
}
