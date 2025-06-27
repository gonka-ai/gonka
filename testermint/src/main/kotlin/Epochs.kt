package com.productscience

import com.productscience.data.EpochParams
import com.productscience.data.EpochResponse
import kotlin.math.truncate

enum class EpochStage {
    START_OF_POC,
    END_OF_POC,
    POC_EXCHANGE_DEADLINE,
    START_OF_POC_VALIDATION,
    END_OF_POC_VALIDATION,
    SET_NEW_VALIDATORS,
    CLAIM_REWARDS
}

fun EpochParams.shift(blockHeight: Long): Long = blockHeight + this.epochShift

fun EpochParams.getStage(stage: EpochStage): Long = when (stage) {
    EpochStage.START_OF_POC -> 0L
    EpochStage.END_OF_POC -> getStage(EpochStage.START_OF_POC) + pocValidationDuration * epochMultiplier
    EpochStage.POC_EXCHANGE_DEADLINE -> getStage(EpochStage.END_OF_POC) + pocExchangeDuration * epochMultiplier
    EpochStage.START_OF_POC_VALIDATION -> getStage(EpochStage.END_OF_POC) + pocValidationDelay * epochMultiplier
    EpochStage.END_OF_POC_VALIDATION -> getStage(EpochStage.START_OF_POC_VALIDATION) + pocValidationDuration * epochMultiplier
    EpochStage.SET_NEW_VALIDATORS -> getStage(EpochStage.END_OF_POC_VALIDATION) + 1 * epochMultiplier
    EpochStage.CLAIM_REWARDS -> getStage(EpochStage.SET_NEW_VALIDATORS) + 1 * epochMultiplier
}

fun EpochResponse.getNextStage(stage: EpochStage): Long {
    return when (stage) {
        EpochStage.START_OF_POC -> whateverIsNext(epochStages.pocStart, nextEpochStages.pocStart)
        EpochStage.END_OF_POC -> whateverIsNext(epochStages.pocGenerationEnd, nextEpochStages.pocGenerationEnd)
        EpochStage.POC_EXCHANGE_DEADLINE -> whateverIsNext(epochStages.pocExchangeWindow.end, nextEpochStages.pocExchangeWindow.end)
        EpochStage.START_OF_POC_VALIDATION -> whateverIsNext(epochStages.pocValidationStart, nextEpochStages.pocValidationStart)
        EpochStage.END_OF_POC_VALIDATION -> whateverIsNext(epochStages.pocValidationEnd, nextEpochStages.pocValidationEnd)
        EpochStage.SET_NEW_VALIDATORS -> whateverIsNext(epochStages.setNewValidators, nextEpochStages.setNewValidators)
        EpochStage.CLAIM_REWARDS -> whateverIsNext(epochStages.claimMoney, nextEpochStages.claimMoney)
    }
}

fun EpochResponse.whateverIsNext(a: Long, b: Long): Long {
    assert(a < b)
    return when (blockHeight < a) {
        true -> a
        false -> b
    }
}
