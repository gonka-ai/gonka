package com.productscience

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
