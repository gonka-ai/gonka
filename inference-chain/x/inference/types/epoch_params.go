package types

// PR TODO: validate epoch params and gather hardcoded params from the chain
func (p *EpochParams) IsStartOfPoCStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetStartOfPoCStage()
}

func (p *EpochParams) IsEndOfPoCStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetEndOfPoCStage()
}

func (p *EpochParams) IsPoCExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	startBlockHeight = p.shift(startBlockHeight)
	currentBlockHeight = p.shift(currentBlockHeight)
	elapsedEpochs := currentBlockHeight - startBlockHeight

	return p.isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= p.GetPoCExchangeDeadline()
}

func (p *EpochParams) IsStartOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetStartOfPoCValidationStage()
}

func (p *EpochParams) IsValidationExchangeWindow(startBlockHeight, currentBlockHeight int64) bool {
	startBlockHeight = p.shift(startBlockHeight)
	currentBlockHeight = p.shift(currentBlockHeight)
	elapsedEpochs := currentBlockHeight - startBlockHeight
	return p.isNotZeroEpoch(startBlockHeight) && elapsedEpochs > 0 && elapsedEpochs <= p.GetSetNewValidatorsStage()
}

func (p *EpochParams) IsEndOfPoCValidationStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetEndOfPoCValidationStage()
}

func (p *EpochParams) IsSetNewValidatorsStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetSetNewValidatorsStage()
}

func (p *EpochParams) IsClaimMoneyStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)

	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetClaimMoneyStage()

}

func (p *EpochParams) GetStartBlockHeightFromEndOfPocStage(blockHeight int64) int64 {
	return p.unshift(p.shift(blockHeight) - p.GetEndOfPoCStage())
}

func (p *EpochParams) GetStartBlockHeightFromStartOfPocValidationStage(blockHeight int64) int64 {
	return p.unshift(p.shift(blockHeight) - p.GetStartOfPoCValidationStage())
}

func (p *EpochParams) GetStartOfPoCStage() int64 {
	return 0
}

func (p *EpochParams) GetEndOfPoCStage() int64 {
	return p.GetStartOfPoCStage() + (p.PocStageDuration * p.EpochMultiplier)
}

func (p *EpochParams) GetPoCExchangeDeadline() int64 {
	return p.GetEndOfPoCStage() + (p.PocExchangeDuration * p.EpochMultiplier)
}

// TODO: may be longer period between
func (p *EpochParams) GetStartOfPoCValidationStage() int64 {
	return p.GetEndOfPoCStage() + (p.PocValidationDelay * p.EpochMultiplier)
}

func (p *EpochParams) GetEndOfPoCValidationStage() int64 {
	return p.GetStartOfPoCValidationStage() + (p.PocValidationDuration * p.EpochMultiplier)
}

func (p *EpochParams) GetSetNewValidatorsStage() int64 {
	return p.GetEndOfPoCValidationStage() + p.EpochMultiplier
}

func (p *EpochParams) GetClaimMoneyStage() int64 {
	return p.GetSetNewValidatorsStage() + (1 * p.EpochMultiplier)
}

func (p *EpochParams) isNotZeroEpoch(blockHeight int64) bool {
	return !p.isZeroEpoch(blockHeight)
}

func (p *EpochParams) isZeroEpoch(blockHeight int64) bool {
	return blockHeight < p.EpochLength
}

func (p *EpochParams) shift(blockHeight int64) int64 {
	return blockHeight + p.EpochShift
}

func (p *EpochParams) unshift(blockHeight int64) int64 {
	return blockHeight - p.EpochShift
}
