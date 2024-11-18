package com.productscience


const val Multiplier = 1L
const val EpochLength = 10 * Multiplier
const val startOfPocStage = 0 * Multiplier
const val endOfPocStage = 3 * Multiplier
const val pocExchangeDeadline = endOfPocStage + 5
const val setNewValidatorsStage = pocExchangeDeadline + 1

const val EPOCH_NEW_COIN = 1_048_576L
const val COIN_HALVING_HEIGHT = 100

fun isStartOfPoCStage(blockHeight: Long): Boolean {
    return isNotZeroEpoch(blockHeight) && blockHeight % EpochLength == startOfPocStage
}

fun isEndOfPoCStage(blockHeight: Long): Boolean {
    return isNotZeroEpoch(blockHeight) && blockHeight % EpochLength == endOfPocStage
}

fun isPoCExchangeWindow(startBlockHeight: Long, currentBlockHeight: Long): Boolean {
    val elapsedEpochs = currentBlockHeight - startBlockHeight
    return isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= pocExchangeDeadline
}

fun isSetNewValidatorsStage(blockHeight: Long): Boolean {
    return isNotZeroEpoch(blockHeight) && blockHeight % EpochLength == setNewValidatorsStage
}

fun isNotZeroEpoch(blockHeight: Long): Boolean {
    return blockHeight >= EpochLength
}
