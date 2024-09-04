package proofofcompute

const (
	epochLength           = 240
	startOfPocStage       = 0
	endOfPocStage         = 60
	pocExchangeDeadline   = 63
	setNewValidatorsStage = 69
)

func IsStartOfPocStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == startOfPocStage
}

func IsEndOfPocStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == endOfPocStage
}

func IsPocExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	elapsedEpochs := currentBlockHeight - startBlockHeight
	return isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= pocExchangeDeadline
}

func IsSetNewValidatorsStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == setNewValidatorsStage
}

func isNotZeroEpoch(blockHeight int64) bool {
	return blockHeight >= epochLength
}
