package proofofcompute

const (
	Multiplier            = 1
	EpochLength           = 100 * Multiplier
	startOfPocStage       = 0 * Multiplier
	endOfPocStage         = 20 * Multiplier
	pocExchangeDeadline   = (endOfPocStage + 2) * Multiplier
	startOfPocValStage    = (endOfPocStage + 1) * Multiplier
	endOfPocValStage      = (startOfPocValStage + 20) * Multiplier
	setNewValidatorsStage = (endOfPocValStage + 1) * Multiplier
)

func IsStartOfPoCStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == startOfPocStage
}

func IsEndOfPoCStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == endOfPocStage
}

func IsPoCExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	startBlockHeight = shift(startBlockHeight)
	currentBlockHeight = shift(currentBlockHeight)

	elapsedEpochs := currentBlockHeight - startBlockHeight
	return isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= pocExchangeDeadline
}

func IsStartOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == startOfPocValStage
}

func IsEndOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == endOfPocValStage
}

func IsSetNewValidatorsStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return isNotZeroEpoch(blockHeight) && shift(blockHeight)%EpochLength == setNewValidatorsStage
}

func isNotZeroEpoch(blockHeight int64) bool {
	return blockHeight >= EpochLength
}

func shift(blockHeight int64) int64 {
	// PRTODO: remove the shift!
	return blockHeight + 90
}

func unshift(blockHeight int64) int64 {
	// PRTODO: remove the shift!
	return blockHeight - 90
}

func GetStartBlockHeightFromEndOfPocStage(blockHeight int64) int64 {
	return unshift(shift(blockHeight) - endOfPocStage)
}

func GetStartBlockHeightFromStartOfValStage(blockHeight int64) int64 {
	return unshift(shift(blockHeight) - startOfPocValStage)
}
