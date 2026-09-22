package types

import (
	gogoproto "github.com/cosmos/gogoproto/proto"
)

// SchemeForStage is the leaf recipe for a stage from live params. Call only when
// freezing a PocStageRecipe. After that, generate/validate/evaluate read the store.
// Regular PoC: event == nil → poc_scheme. CPoC EventSequence < K uses slot 17.
func SchemeForStage(p *PocParams, event *ConfirmationPoCEvent) PocScheme {
	if p == nil {
		return PocScheme_POC_SCHEME_PREFILL
	}
	if event != nil && p.ConfirmationSchemeEvents > 0 &&
		event.EventSequence < uint64(p.ConfirmationSchemeEvents) {
		return p.ConfirmationPocScheme
	}
	return p.PocScheme
}

// IsSchemeTracking is dry-run CPoC: the event's scheme differs from regular PoC.
// Regular PoC (event == nil) is never tracking.
func IsSchemeTracking(p *PocParams, event *ConfirmationPoCEvent) bool {
	if p == nil || event == nil {
		return false
	}
	return SchemeForStage(p, event) != p.PocScheme
}

// IsSchemeTrackingWithGrace also dry-runs CPoC in the epoch of a poc_scheme change.
func IsSchemeTrackingWithGrace(p *PocParams, event *ConfirmationPoCEvent, epoch, graceEpoch uint64, graceFound bool) bool {
	if event == nil {
		return false
	}
	if IsSchemeTracking(p, event) {
		return true
	}
	return graceFound && epoch == graceEpoch
}

// DecodeMaxForStage is N for DECODE stages and 0 for PREFILL even if the model has N set.
func DecodeMaxForStage(scheme PocScheme, decodeMaxTokens int64) int64 {
	if scheme != PocScheme_POC_SCHEME_DECODE {
		return 0
	}
	return decodeMaxTokens
}

// StatTestForScheme returns the DECODE triple when present, otherwise prefill L2.
func StatTestForScheme(scheme PocScheme, model *PoCModelConfig) *PoCStatTestParams {
	if model == nil {
		return nil
	}
	if scheme == PocScheme_POC_SCHEME_DECODE && model.DecodeStatTest != nil {
		return model.DecodeStatTest
	}
	return model.StatTest
}

// SnapshotPocStageRecipe copies live params into a write-once stage snapshot.
func SnapshotPocStageRecipe(
	p *PocParams,
	event *ConfirmationPoCEvent,
	stageHeight int64,
	epoch, graceEpoch uint64,
	graceFound bool,
) *PocStageRecipe {
	if p == nil {
		p = &PocParams{}
	}
	models := make([]*PoCModelConfig, 0, len(p.GetModelConfigs()))
	for _, m := range p.GetModelConfigs() {
		if m == nil {
			continue
		}
		models = append(models, gogoproto.Clone(m).(*PoCModelConfig))
	}
	return &PocStageRecipe{
		StageHeight:           stageHeight,
		Scheme:                SchemeForStage(p, event),
		Tracking:              IsSchemeTrackingWithGrace(p, event, epoch, graceEpoch, graceFound),
		PocStrongerRngEnabled: p.PocStrongerRngEnabled,
		Models:                models,
	}
}

// ApplyStageRecipe returns a PocParams view of a frozen recipe so generate/validate
// can keep using GetModelConfig / PocScheme without live gov values.
func ApplyStageRecipe(p *PocParams, recipe *PocStageRecipe) *PocParams {
	if p == nil {
		p = &PocParams{}
	}
	out := gogoproto.Clone(p).(*PocParams)
	if recipe == nil {
		return out
	}
	out.PocScheme = recipe.Scheme
	out.ConfirmationSchemeEvents = 0
	out.PocStrongerRngEnabled = recipe.PocStrongerRngEnabled
	out.Models = recipe.Models
	return out
}

func (r *PocStageRecipe) GetModelConfig(modelID string) (*PoCModelConfig, bool) {
	if r == nil {
		return nil, false
	}
	for _, config := range r.Models {
		if config != nil && config.ModelId == modelID {
			return config, true
		}
	}
	return nil, false
}

func pocSlotIsDecode(p *PocParams) bool {
	if p == nil {
		return false
	}
	if p.PocScheme == PocScheme_POC_SCHEME_DECODE {
		return true
	}
	return p.ConfirmationSchemeEvents > 0 && p.ConfirmationPocScheme == PocScheme_POC_SCHEME_DECODE
}
