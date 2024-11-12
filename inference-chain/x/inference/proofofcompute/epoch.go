package proofofcompute

const (
	Multiplier            = 1
	EpochLength           = 10 * Multiplier
	startOfPocStage       = 0 * Multiplier
	endOfPocStage         = 5 * Multiplier
	pocExchangeDeadline   = endOfPocStage + 2
	setNewValidatorsStage = pocExchangeDeadline + 1
)

func IsStartOfPoCStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == startOfPocStage
}

func IsEndOfPoCStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == endOfPocStage
}

func IsPoCExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	elapsedEpochs := currentBlockHeight - startBlockHeight
	return isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= pocExchangeDeadline
}

func IsSetNewValidatorsStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%EpochLength == setNewValidatorsStage
}

func isNotZeroEpoch(blockHeight int64) bool {
	return blockHeight >= EpochLength
}
