package types

import (
	"fmt"

	gogoproto "github.com/cosmos/gogoproto/proto"
)

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

// activePocSchemes includes the first confirmation event's DECODE trial while
// PREFILL is the regular mode. DECODE is the permanent, single-scheme mode.
func activePocSchemes(p *PocParams) (map[PocScheme]struct{}, error) {
	scheme := p.GetPocScheme()
	if !knownPocScheme(scheme) {
		return nil, fmt.Errorf("unknown poc_scheme %d", scheme)
	}
	required := map[PocScheme]struct{}{PocScheme_POC_SCHEME_DECODE: {}}
	if scheme == PocScheme_POC_SCHEME_PREFILL {
		required[scheme] = struct{}{}
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
		if needDecode && !needPrefill {
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

// SnapshotPocStageRecipe freezes the epoch's recipes at regular PoC start.
// PREFILL keeps both blocks for its first confirmation event's DECODE trial.
func SnapshotPocStageRecipe(p *PocParams, stageHeight int64) (*PocStageRecipe, error) {
	required, err := activePocSchemes(p)
	if err != nil {
		return nil, err
	}
	recipe := &PocStageRecipe{
		StageHeight:           stageHeight,
		Scheme:                p.GetPocScheme(),
		PocStrongerRngEnabled: p.GetPocStrongerRngEnabled(),
	}
	for _, model := range p.GetModelConfigs() {
		if model == nil {
			return nil, fmt.Errorf("nil PoC model")
		}
		cloned := gogoproto.Clone(model).(*PoCModelConfig)
		cloned.Schemes = nil
		for _, scheme := range []PocScheme{PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE} {
			if _, needed := required[scheme]; !needed {
				continue
			}
			block, ok := model.SchemeParams(scheme)
			if !ok {
				return nil, fmt.Errorf("model %q has no %s scheme block", model.ModelId, scheme)
			}
			cloned.Schemes = append(cloned.Schemes, gogoproto.Clone(block).(*PocSchemeParams))
		}
		recipe.Models = append(recipe.Models, cloned)
	}
	return recipe, nil
}

// ConfirmationRecipe derives an event from its epoch, never from live params.
// The first confirmation in transitional PREFILL mode only trials DECODE.
func (r *PocStageRecipe) ConfirmationRecipe(event *ConfirmationPoCEvent) (*PocStageRecipe, error) {
	out := gogoproto.Clone(r).(*PocStageRecipe)
	out.StageHeight = event.TriggerHeight
	out.Tracking = r.Scheme == PocScheme_POC_SCHEME_PREFILL && event.EventSequence == 0
	if out.Tracking {
		out.Scheme = PocScheme_POC_SCHEME_DECODE
	}
	for _, model := range out.Models {
		block, ok := model.SchemeParams(out.Scheme)
		if !ok {
			return nil, fmt.Errorf("model %q has no %s scheme block", model.ModelId, out.Scheme)
		}
		model.Schemes = []*PocSchemeParams{block}
	}
	return out, nil
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
