package proofofcompute

const epochLength = 240

func IsStartOfPocStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == 0
}

func IsEndOfPocStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == 60
}

func IsPocExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	elapsedEpochs := currentBlockHeight - startBlockHeight
	return isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= 63
}

func IsSetNewValidatorsStage(blockHeight int64) bool {
	return isNotZeroEpoch(blockHeight) && blockHeight%epochLength == 69
}

func isNotZeroEpoch(blockHeight int64) bool {
	return blockHeight >= epochLength
}
