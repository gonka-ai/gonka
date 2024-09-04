package proofofcompute

const (
	EpochLength           = 240
	startOfPocStage       = 0
	endOfPocStage         = 60
	pocExchangeDeadline   = 63
	setNewValidatorsStage = 69
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
