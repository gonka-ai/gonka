package proofofcompute

const epochLength = 240

func IsStartOfPocStage(blockHeight uint64) bool {
	return blockHeight%epochLength == 0
}

func IsEndOfPocStage(blockHeight uint64) bool {
	return blockHeight%epochLength == 60
}

func IsPocExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	elapsedEpochs := currentBlockHeight - startBlockHeight
	return elapsedEpochs > 0 && elapsedEpochs <= 63
}

func IsSetNewValidatorsStage(blockHeight int64) bool {
	return blockHeight%epochLength == 69
}
