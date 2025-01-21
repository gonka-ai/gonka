package types

func (p *EpochParams) IsStartOfPoCStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetStartOfPoCStage()
}

func (p *EpochParams) IsEndOfPoCStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetEndOfPoCStage()
}

func (p *EpochParams) IsPoCExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	startBlockHeight = shift(startBlockHeight)
	currentBlockHeight = shift(currentBlockHeight)

	elapsedEpochs := currentBlockHeight - startBlockHeight
	return p.isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= p.GetPoCExchangeDeadline()
}

func (p *EpochParams) IsStartOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetStartOfPoCValidationStage()
}

func (p *EpochParams) IsValidationExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	startBlockHeight = shift(startBlockHeight)
	currentBlockHeight = shift(currentBlockHeight)

	elapsedEpochs := currentBlockHeight - startBlockHeight
	return p.isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= p.GetSetNewValidatorsStage()
}

func (p *EpochParams) IsEndOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetEndOfPoCValidationStage()
}

func (p *EpochParams) IsSetNewValidatorsStage(blockHeight int64) bool {
	blockHeight = shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetSetNewValidatorsStage()
}

func (p *EpochParams) GetStartBlockHeightFromEndOfPocStage(blockHeight int64) int64 {
	return unshift(shift(blockHeight) - p.GetEndOfPoCStage())
}

func (p *EpochParams) GetStartBlockHeightFromStartOfPocValidationStage(blockHeight int64) int64 {
	return unshift(shift(blockHeight) - p.GetStartOfPoCValidationStage())
}

func (p *EpochParams) GetStartOfPoCStage() int64 {
	return 0 * p.EpochMultiplier
}

func (p *EpochParams) GetEndOfPoCStage() int64 {
	return 10 * p.EpochMultiplier
}

func (p *EpochParams) GetPoCExchangeDeadline() int64 {
	return (p.GetEndOfPoCStage() + 2) * p.EpochMultiplier
}

func (p *EpochParams) GetStartOfPoCValidationStage() int64 {
	return (p.GetEndOfPoCStage() + 2) * p.EpochMultiplier
}

func (p *EpochParams) GetEndOfPoCValidationStage() int64 {
	return (p.GetStartOfPoCValidationStage() + 4) * p.EpochMultiplier
}

func (p *EpochParams) GetSetNewValidatorsStage() int64 {
	return (p.GetEndOfPoCValidationStage() + 1) * p.EpochMultiplier
}

func (p *EpochParams) isNotZeroEpoch(blockHeight int64) bool {
	return !p.IsZeroEpoch(blockHeight)
}

func (p *EpochParams) IsZeroEpoch(blockHeight int64) bool {
	return blockHeight < int64(p.EpochLength)
}

const shiftVal = 0

func shift(blockHeight int64) int64 {
	return blockHeight + shiftVal
}

func unshift(blockHeight int64) int64 {
	return blockHeight - shiftVal
}
