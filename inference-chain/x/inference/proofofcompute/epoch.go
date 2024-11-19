package proofofcompute

const (
	Multiplier            = 1
	EpochLength           = 100 * Multiplier
	startOfPocStage       = 0 * Multiplier
	endOfPocStage         = 20 * Multiplier
	pocExchangeDeadline   = endOfPocStage + 2
	setNewValidatorsStage = pocExchangeDeadline + 1
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
