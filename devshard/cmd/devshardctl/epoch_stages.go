package main

import "strings"

// Epoch stage offsets mirror inference-chain/x/inference/types/epoch_params.go
// so the gateway can derive set_new_validators / next_poc_start / phase from
// existing chain EpochInfo (params + latest_epoch) without new chain queries
// and without depending on decentralized-api.
const (
	pocGenerateWindDownFactor = 0.8
	pocValidateWindDownFactor = 0.8
)

type chainEpochParams struct {
	EpochLength            jsonInt64 `json:"epoch_length"`
	EpochShift             jsonInt64 `json:"epoch_shift"`
	PocStageDuration       jsonInt64 `json:"poc_stage_duration"`
	PocExchangeDuration    jsonInt64 `json:"poc_exchange_duration"`
	PocValidationDelay     jsonInt64 `json:"poc_validation_delay"`
	PocValidationDuration  jsonInt64 `json:"poc_validation_duration"`
	SetNewValidatorsDelay  jsonInt64 `json:"set_new_validators_delay"`
	InferenceValidationCut jsonInt64 `json:"inference_validation_cutoff"`
}

func (p chainEpochParams) endOfPoCStage() int64 {
	return int64(p.PocStageDuration)
}

func (p chainEpochParams) startOfPoCValidationStage() int64 {
	return p.endOfPoCStage() + int64(p.PocValidationDelay)
}

func (p chainEpochParams) endOfPoCValidationStage() int64 {
	return p.startOfPoCValidationStage() + int64(p.PocValidationDuration)
}

func (p chainEpochParams) setNewValidatorsStage() int64 {
	return p.endOfPoCValidationStage() + int64(p.SetNewValidatorsDelay)
}

func (p chainEpochParams) setNewValidatorsHeight(pocStart int64, epochIndex uint64) int64 {
	if epochIndex == 0 {
		return 0
	}
	return pocStart + p.setNewValidatorsStage()
}

func (p chainEpochParams) nextPoCStart(pocStart int64, epochIndex uint64) int64 {
	if epochIndex == 0 {
		return -int64(p.EpochShift) + int64(p.EpochLength)
	}
	return pocStart + int64(p.EpochLength)
}

func deriveEpochStages(epoch chainLatestEpoch, params chainEpochParams) (current, next chainEpochStages) {
	index := uint64(epoch.Index)
	pocStart := int64(epoch.PocStartBlockHeight)
	current = chainEpochStages{
		EpochIndex:       jsonUint64(index),
		SetNewValidators: jsonInt64(params.setNewValidatorsHeight(pocStart, index)),
		NextPoCStart:     jsonInt64(params.nextPoCStart(pocStart, index)),
	}
	nextPocStart := int64(current.NextPoCStart)
	next = chainEpochStages{
		EpochIndex:       jsonUint64(index + 1),
		SetNewValidators: jsonInt64(params.setNewValidatorsHeight(nextPocStart, index+1)),
		NextPoCStart:     jsonInt64(params.nextPoCStart(nextPocStart, index+1)),
	}
	return current, next
}

// deriveEpochPhaseFromParams mirrors EpochContext.GetCurrentPhase.
func deriveEpochPhaseFromParams(blockHeight int64, epoch chainLatestEpoch, params chainEpochParams) string {
	index := uint64(epoch.Index)
	if index == 0 {
		return epochPhaseInference
	}
	pocStart := int64(epoch.PocStartBlockHeight)
	if blockHeight < pocStart {
		return epochPhaseInference
	}
	windDown := pocStart + int64(float64(params.PocStageDuration)*pocGenerateWindDownFactor)
	validationStart := pocStart + params.startOfPoCValidationStage()
	validationWindDown := pocStart + params.startOfPoCValidationStage() + int64(float64(params.PocValidationDuration)*pocValidateWindDownFactor)
	validationEnd := pocStart + params.endOfPoCValidationStage()

	switch {
	case blockHeight >= pocStart && blockHeight < windDown:
		return epochPhasePoCGenerate
	case blockHeight >= windDown && blockHeight < validationStart:
		return epochPhasePoCGenerateWindDown
	case blockHeight >= validationStart && blockHeight < validationWindDown:
		return epochPhasePoCValidate
	case blockHeight >= validationWindDown && blockHeight < validationEnd:
		return epochPhasePoCValidateWindDown
	default:
		return epochPhaseInference
	}
}

// enrichEpochInfoStages fills phase/stages from EpochInfo params when the LCD
// response does not include precomputed fields (current mainnet EpochInfo).
func enrichEpochInfoStages(payload *chainEpochInfoResponse) {
	if payload == nil {
		return
	}
	params := payload.Params.EpochParams
	current, next := deriveEpochStages(payload.LatestEpoch, params)
	if payload.EpochStages.SetNewValidators == 0 && payload.EpochStages.NextPoCStart == 0 {
		payload.EpochStages = current
	}
	if payload.NextEpochStages.SetNewValidators == 0 {
		payload.NextEpochStages = next
	}
	if strings.TrimSpace(payload.Phase) == "" {
		payload.Phase = deriveEpochPhaseFromParams(int64(payload.BlockHeight), payload.LatestEpoch, params)
	}
}
