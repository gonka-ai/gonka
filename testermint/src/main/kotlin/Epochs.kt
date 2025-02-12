package com.productscience

import com.productscience.data.EpochParams

private const val SHIFT_VAL = 0L

private fun shift(blockHeight: Long): Long = blockHeight + SHIFT_VAL
private fun unshift(blockHeight: Long): Long = blockHeight - SHIFT_VAL

fun EpochParams.isStartOfPoCStage(blockHeight: Long): Boolean {
    val shiftedBlockHeight = shift(blockHeight)
    return this.isNotZeroEpoch(shiftedBlockHeight) &&
            (shiftedBlockHeight % epochLength == this.getStartOfPoCStage())
}

fun EpochParams.isEndOfPoCStage(blockHeight: Long): Boolean {
    val shiftedBlockHeight = shift(blockHeight)
    return this.isNotZeroEpoch(shiftedBlockHeight) &&
            (shiftedBlockHeight % epochLength == this.getEndOfPoCStage())
}

fun EpochParams.isPoCExchangeWindow(startBlockHeight: Long, currentBlockHeight: Long): Boolean {
    val shiftedStart = shift(startBlockHeight)
    val shiftedCurrent = shift(currentBlockHeight)
    val elapsedEpochs = shiftedCurrent - shiftedStart
    return this.isNotZeroEpoch(shiftedStart) &&
            (elapsedEpochs > 0) &&
            (elapsedEpochs <= this.getPoCExchangeDeadline())
}

fun EpochParams.isStartOfPoCValidationStage(blockHeight: Long): Boolean {
    val shiftedBlockHeight = shift(blockHeight)
    return this.isNotZeroEpoch(shiftedBlockHeight) &&
            (shiftedBlockHeight % epochLength == this.getStartOfPoCValidationStage())
}

fun EpochParams.isValidationExchangeWindow(startBlockHeight: Long, currentBlockHeight: Long): Boolean {
    val shiftedStart = shift(startBlockHeight)
    val shiftedCurrent = shift(currentBlockHeight)
    val elapsedEpochs = shiftedCurrent - shiftedStart
    return this.isNotZeroEpoch(shiftedStart) &&
            (elapsedEpochs > 0) &&
            (elapsedEpochs <= this.getSetNewValidatorsStage())
}

fun EpochParams.isEndOfPoCValidationStage(blockHeight: Long): Boolean {
    val shiftedBlockHeight = shift(blockHeight)
    return this.isNotZeroEpoch(shiftedBlockHeight) &&
            (shiftedBlockHeight % epochLength == this.getEndOfPoCValidationStage())
}

fun EpochParams.isSetNewValidatorsStage(blockHeight: Long): Boolean {
    val shiftedBlockHeight = shift(blockHeight)
    return this.isNotZeroEpoch(shiftedBlockHeight) &&
            (shiftedBlockHeight % epochLength == this.getSetNewValidatorsStage())
}

fun EpochParams.getStartBlockHeightFromEndOfPocStage(blockHeight: Long): Long {
    return unshift(shift(blockHeight) - this.getEndOfPoCStage())
}

fun EpochParams.getStartBlockHeightFromStartOfPocValidationStage(blockHeight: Long): Long {
    return unshift(shift(blockHeight) - this.getStartOfPoCValidationStage())
}

fun EpochParams.getStartOfPoCStage(): Long = 0L

fun EpochParams.getEndOfPoCStage(): Long = 10L * epochMultiplier

fun EpochParams.getPoCExchangeDeadline(): Long = (this.getEndOfPoCStage() + 2) * epochMultiplier

fun EpochParams.getStartOfPoCValidationStage(): Long = (this.getEndOfPoCStage() + 2) * epochMultiplier

fun EpochParams.getEndOfPoCValidationStage(): Long = (this.getStartOfPoCValidationStage() + 4) * epochMultiplier

fun EpochParams.getSetNewValidatorsStage(): Long = (this.getEndOfPoCValidationStage() + 1) * epochMultiplier

fun EpochParams.isNotZeroEpoch(blockHeight: Long): Boolean = !this.isZeroEpoch(blockHeight)

fun EpochParams.isZeroEpoch(blockHeight: Long): Boolean = blockHeight < epochLength
