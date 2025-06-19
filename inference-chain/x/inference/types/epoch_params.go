package types

const (
	// PoCGenerateWindDownFactor determines the start of the "wind down" period for PoC generation, as a percentage of the PoC stage duration.
	PoCGenerateWindDownFactor = 0.8
	// PoCValidateWindDownFactor determines the start of the "wind down" period for PoC validation, as a percentage of the PoC validation stage duration.
	PoCValidateWindDownFactor = 0.8
)

type EpochPhase string

const (
	PoCGeneratePhase         EpochPhase = "PoCGenerate"
	PoCGenerateWindDownPhase EpochPhase = "PoCGenerateWindDown"
	PoCValidatePhase         EpochPhase = "PoCValidate"
	PoCValidateWindDownPhase EpochPhase = "PoCValidateWindDown"
	InferencePhase           EpochPhase = "Inference"
)

// PR TODO: validate epoch params and gather hardcoded params from the chain
func (p *EpochParams) IsStartOfPoCStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.getStartOfPocStage()
}

func (p *EpochParams) IsEndOfPoCStage(blockHeight int64) bool {
	blockHeight = p.shift(blockHeight)
	return p.isNotZeroEpoch(blockHeight) && blockHeight%p.EpochLength == p.GetEndOfPoCStage()
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

func (p *EpochParams) getStartOfPocStage() int64 {
	return 0
}

func (p *EpochParams) GetPoCWinddownStage() int64 {
	return p.getStartOfPocStage() + (int64(float64(p.PocStageDuration)*PoCGenerateWindDownFactor) * p.EpochMultiplier)
}

func (p *EpochParams) GetEndOfPoCStage() int64 {
	return p.getStartOfPocStage() + (p.PocStageDuration * p.EpochMultiplier)
}

func (p *EpochParams) GetPoCExchangeDeadline() int64 {
	return p.GetEndOfPoCStage() + (p.PocExchangeDuration * p.EpochMultiplier)
}

// TODO: may be longer period between
func (p *EpochParams) GetStartOfPoCValidationStage() int64 {
	return p.GetEndOfPoCStage() + (p.PocValidationDelay * p.EpochMultiplier)
}

func (p *EpochParams) GetPoCValidationWindownStage() int64 {
	return p.GetStartOfPoCValidationStage() + (int64(float64(p.PocValidationDuration)*PoCValidateWindDownFactor) * p.EpochMultiplier)
}

func (p *EpochParams) GetEndOfPoCValidationStage() int64 {
	return p.GetStartOfPoCValidationStage() + (p.PocValidationDuration * p.EpochMultiplier)
}

func (p *EpochParams) GetSetNewValidatorsStage() int64 {
	return p.GetEndOfPoCValidationStage() + (1 * p.EpochMultiplier)
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
