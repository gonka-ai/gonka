package types

import (
	"fmt"

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

// SchemeParams returns the recipe block for scheme.
// A populated schemes list never falls back to another scheme.
// An empty list still reads the flat prefill fields (flat genesis and the
// post-v0.2.16 store; the v0.2.17 upgrade moves mainnet into schemes[PREFILL]).
// DECODE has no flat-field fallback: it is not deployed, so a missing block is missing.
func (m *PoCModelConfig) SchemeParams(scheme PocScheme) (*PocSchemeParams, bool) {
	if m == nil {
		return nil, false
	}
	for _, block := range m.Schemes {
		if block != nil && block.Scheme == scheme {
			return block, true
		}
	}
	if len(m.Schemes) > 0 || scheme != PocScheme_POC_SCHEME_PREFILL {
		return nil, false
	}
	return &PocSchemeParams{
		Scheme:    PocScheme_POC_SCHEME_PREFILL,
		SeqLen:    m.SeqLen,
		MaxTokens: 0,
		StatTest:  m.StatTest,
	}, true
}

// DynamicCoefficientFor is the coefficient config of the model's block for scheme, or nil.
func (m *PoCModelConfig) DynamicCoefficientFor(scheme PocScheme) *DynamicCoefficientModelConfig {
	if m == nil {
		return nil
	}
	for _, block := range m.Schemes {
		if block != nil && block.Scheme == scheme && block.DynamicCoefficient != nil {
			return block.DynamicCoefficient
		}
	}
	if scheme == PocScheme_POC_SCHEME_PREFILL {
		return m.DynamicCoefficient
	}
	return nil
}

// DecodeMaxForStage is N for DECODE stages and 0 for PREFILL even if the model has N set.
func DecodeMaxForStage(scheme PocScheme, decodeMaxTokens int64) int64 {
	if scheme != PocScheme_POC_SCHEME_DECODE {
		return 0
	}
	return decodeMaxTokens
}

// StatTestForScheme returns the verdict triple for scheme.
func StatTestForScheme(scheme PocScheme, model *PoCModelConfig) *PoCStatTestParams {
	if model == nil {
		return nil
	}
	block, ok := model.SchemeParams(scheme)
	if !ok || block == nil {
		return nil
	}
	return block.StatTest
}

// MaxTokensForScheme is the decode step count for a DECODE block and 0 for PREFILL.
// ok is false when that scheme's block is missing.
func (m *PoCModelConfig) MaxTokensForScheme(scheme PocScheme) (int64, bool) {
	block, ok := m.SchemeParams(scheme)
	if !ok || block == nil {
		return 0, false
	}
	if scheme != PocScheme_POC_SCHEME_DECODE {
		return 0, true
	}
	return block.MaxTokens, true
}

// activePocSchemes is the regular slot plus the confirmation slot when K > 0.
func activePocSchemes(p *PocParams) (map[PocScheme]struct{}, error) {
	required := map[PocScheme]struct{}{}
	if p == nil {
		required[PocScheme_POC_SCHEME_PREFILL] = struct{}{}
		return required, nil
	}
	if !knownPocScheme(p.PocScheme) {
		return nil, fmt.Errorf("poc_params.poc_scheme has unknown value %d", p.PocScheme)
	}
	required[p.PocScheme] = struct{}{}
	if p.ConfirmationSchemeEvents > 0 {
		if !knownPocScheme(p.ConfirmationPocScheme) {
			return nil, fmt.Errorf("poc_params.confirmation_poc_scheme has unknown value %d", p.ConfirmationPocScheme)
		}
		required[p.ConfirmationPocScheme] = struct{}{}
	}
	return required, nil
}

func knownPocScheme(scheme PocScheme) bool {
	switch scheme {
	case PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE:
		return true
	default:
		return false
	}
}

// validateSchemeBlocks checks the scheme list, or the flat prefill fields when it is empty.
// required is every slot that will run. DECODE must be an explicit block.
// PREFILL may stay on the flat fields while schemes is empty.
func (m *PoCModelConfig) validateSchemeBlocks(required map[PocScheme]struct{}) error {
	if m == nil {
		return fmt.Errorf("poc_params.models contains a nil entry")
	}
	_, needPrefill := required[PocScheme_POC_SCHEME_PREFILL]
	_, needDecode := required[PocScheme_POC_SCHEME_DECODE]
	if len(m.Schemes) == 0 {
		if m.SeqLen < 0 {
			return fmt.Errorf("poc_params.models.seq_len cannot be negative")
		}
		if needDecode {
			return fmt.Errorf("model %q requires a DECODE scheme block", m.ModelId)
		}
		return nil
	}
	seen := map[PocScheme]struct{}{}
	for _, block := range m.Schemes {
		if block == nil {
			return fmt.Errorf("model %q has a nil scheme block", m.ModelId)
		}
		switch block.Scheme {
		case PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE:
		default:
			return fmt.Errorf("model %q has unknown scheme %d", m.ModelId, block.Scheme)
		}
		if _, dup := seen[block.Scheme]; dup {
			return fmt.Errorf("model %q has a duplicate %s scheme block", m.ModelId, block.Scheme.String())
		}
		seen[block.Scheme] = struct{}{}
		if block.SeqLen <= 0 {
			return fmt.Errorf("model %q %s seq_len must be > 0", m.ModelId, block.Scheme.String())
		}
		if block.Scheme == PocScheme_POC_SCHEME_DECODE && block.MaxTokens <= 0 {
			return fmt.Errorf("model %q DECODE max_tokens must be > 0", m.ModelId)
		}
		if block.Scheme == PocScheme_POC_SCHEME_PREFILL && block.MaxTokens != 0 {
			return fmt.Errorf("model %q PREFILL max_tokens must be 0", m.ModelId)
		}
	}
	if needPrefill {
		if _, ok := seen[PocScheme_POC_SCHEME_PREFILL]; !ok {
			return fmt.Errorf("model %q requires a PREFILL scheme block", m.ModelId)
		}
	}
	if needDecode {
		if _, ok := seen[PocScheme_POC_SCHEME_DECODE]; !ok {
			return fmt.Errorf("model %q requires a DECODE scheme block", m.ModelId)
		}
	}
	return nil
}

// SnapshotPocStageRecipe copies live params into a write-once stage snapshot.
// A model without a block for the stage scheme rejects the whole snapshot.
func SnapshotPocStageRecipe(
	p *PocParams,
	event *ConfirmationPoCEvent,
	stageHeight int64,
	epoch, graceEpoch uint64,
	graceFound bool,
) (*PocStageRecipe, error) {
	if p == nil {
		p = &PocParams{}
	}
	scheme := SchemeForStage(p, event)
	models := make([]*PoCModelConfig, 0, len(p.GetModelConfigs()))
	for _, m := range p.GetModelConfigs() {
		if m == nil {
			continue
		}
		cloned := gogoproto.Clone(m).(*PoCModelConfig)
		block, ok := cloned.SchemeParams(scheme)
		if !ok || block == nil {
			return nil, fmt.Errorf("model %q has no %s scheme block", cloned.ModelId, scheme.String())
		}
		cloned.Schemes = []*PocSchemeParams{gogoproto.Clone(block).(*PocSchemeParams)}
		models = append(models, cloned)
	}
	return &PocStageRecipe{
		StageHeight:           stageHeight,
		Scheme:                scheme,
		Tracking:              IsSchemeTrackingWithGrace(p, event, epoch, graceEpoch, graceFound),
		PocStrongerRngEnabled: p.PocStrongerRngEnabled,
		Models:                models,
	}, nil
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
