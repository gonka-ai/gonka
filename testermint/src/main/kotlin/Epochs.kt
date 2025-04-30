package com.productscience

import com.productscience.data.EpochParams

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
fun EpochParams.unshift(blockHeight: Long): Long = blockHeight - this.epochShift

fun EpochParams.isStage(stage: EpochStage, blockHeight: Long): Boolean =
    shift(blockHeight) % epochLength == getStage(stage)

fun EpochParams.getStage(stage: EpochStage): Long = when (stage) {
    EpochStage.START_OF_POC -> 0L
    EpochStage.END_OF_POC -> getStage(EpochStage.START_OF_POC) + pocValidationDuration * epochMultiplier
    EpochStage.POC_EXCHANGE_DEADLINE -> getStage(EpochStage.END_OF_POC) + pocExchangeDuration * epochMultiplier
    EpochStage.START_OF_POC_VALIDATION -> getStage(EpochStage.END_OF_POC) + pocValidationDelay * epochMultiplier
    EpochStage.END_OF_POC_VALIDATION -> getStage(EpochStage.START_OF_POC_VALIDATION) + pocValidationDuration * epochMultiplier
    EpochStage.SET_NEW_VALIDATORS -> getStage(EpochStage.END_OF_POC_VALIDATION) + 1 * epochMultiplier
    EpochStage.CLAIM_REWARDS -> getStage(EpochStage.SET_NEW_VALIDATORS) + 1 * epochMultiplier
}
