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

func (p *EpochParams) getStartOfPocStage() int64 {
	return 0
}

func (p *EpochParams) GetPoCWindDownStage() int64 {
	return p.getStartOfPocStage() + int64(float64(p.PocStageDuration)*PoCGenerateWindDownFactor)
}

func (p *EpochParams) GetEndOfPoCStage() int64 {
	return p.getStartOfPocStage() + p.PocStageDuration
}

func (p *EpochParams) GetPoCExchangeDeadline() int64 {
	return p.GetEndOfPoCStage() + p.PocExchangeDuration
}

// TODO: may be longer period between
func (p *EpochParams) GetStartOfPoCValidationStage() int64 {
	return p.GetEndOfPoCStage() + p.PocValidationDelay
}

func (p *EpochParams) GetPoCValidationWindDownStage() int64 {
	return p.GetStartOfPoCValidationStage() + int64(float64(p.PocValidationDuration)*PoCValidateWindDownFactor)
}

func (p *EpochParams) GetEndOfPoCValidationStage() int64 {
	return p.GetStartOfPoCValidationStage() + p.PocValidationDuration
}

func (p *EpochParams) GetSetNewValidatorsStage() int64 {
	return p.GetEndOfPoCValidationStage() + p.SetNewValidatorsDelay
}

func (p *EpochParams) GetClaimMoneyStage() int64 {
	return p.GetSetNewValidatorsStage() + 1
}

func (p *EpochParams) isNotZeroEpoch(blockHeight int64) bool {
	return !p.isZeroEpoch(blockHeight)
}

func (p *EpochParams) isZeroEpoch(blockHeight int64) bool {
	return blockHeight < p.EpochLength
}
