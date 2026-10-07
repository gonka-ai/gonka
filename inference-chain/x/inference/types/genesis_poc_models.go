package types

import gogoproto "github.com/cosmos/gogoproto/proto"

// PrepareGenesisPocModels gives a fresh chain one PREFILL block per governance
// model. A new chain does not run the v0.2.17 upgrade, and DAPI mines only from
// poc_params.models. An empty model_id placeholder, or a genesis that only has
// the model list and the old top-level model_id, is rewritten here.
//
// Models that already cover every id and whose PREFILL shares sum to 10000 are
// left alone, aside from copying a flat prefill recipe onto the scheme block.
func PrepareGenesisPocModels(params *Params, modelIDs []string) {
	if params == nil {
		return
	}
	if params.PocParams == nil {
		params.PocParams = DefaultPocParams()
	}
	ids := uniqueModelIDs(modelIDs)
	if len(ids) == 0 {
		return
	}
	p := params.PocParams
	if p.DynamicCoefficientParams == nil {
		p.DynamicCoefficientParams = DefaultDynamicCoefficientParams()
	}
	seqLen := p.SeqLen
	if seqLen <= 0 {
		seqLen = 256
	}
	stat := p.StatTest
	if stat == nil {
		stat = DefaultPoCStatTestParams()
	}

	kept := make([]*PoCModelConfig, 0, len(ids))
	index := make(map[string]struct{}, len(ids))
	for _, model := range p.Models {
		if model == nil || model.ModelId == "" {
			continue
		}
		if _, ok := index[model.ModelId]; ok {
			continue
		}
		index[model.ModelId] = struct{}{}
		kept = append(kept, model)
	}
	for _, id := range ids {
		if _, ok := index[id]; ok {
			continue
		}
		kept = append(kept, &PoCModelConfig{
			ModelId:  id,
			SeqLen:   seqLen,
			StatTest: clonePoCStatTest(stat),
		})
		index[id] = struct{}{}
	}
	for _, model := range kept {
		ensureGenesisPrefillBlock(model, seqLen, stat)
	}
	if !genesisPrefillSharesReady(kept) {
		assignGenesisPrefillShares(kept, p.DynamicCoefficientParams.TargetZoneBps)
	}
	p.Models = kept
}

// EnableGenesisPocV2 turns on v2 commits for a fresh chain. The v0.2.9 upgrade
// sets these flags for an existing chain; InitGenesis does not run that upgrade,
// and the current API only submits v2 commits.
func EnableGenesisPocV2(params *Params) {
	if params == nil {
		return
	}
	if params.PocParams == nil {
		params.PocParams = DefaultPocParams()
	}
	params.PocParams.PocV2Enabled = true
	params.PocParams.ConfirmationPocV2Enabled = true
}

func uniqueModelIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func ensureGenesisPrefillBlock(model *PoCModelConfig, seqLen int64, stat *PoCStatTestParams) {
	if model.SeqLen <= 0 {
		model.SeqLen = seqLen
	}
	if model.StatTest == nil {
		model.StatTest = clonePoCStatTest(stat)
	}
	var block *PocSchemeParams
	for _, candidate := range model.Schemes {
		if candidate != nil && candidate.Scheme == PocScheme_POC_SCHEME_PREFILL {
			block = candidate
			break
		}
	}
	if block == nil {
		block = &PocSchemeParams{Scheme: PocScheme_POC_SCHEME_PREFILL}
		model.Schemes = append([]*PocSchemeParams{block}, model.Schemes...)
	}
	if block.SeqLen <= 0 {
		block.SeqLen = model.SeqLen
	}
	block.MaxTokens = 0
	if block.StatTest == nil {
		block.StatTest = clonePoCStatTest(model.StatTest)
	}
	if block.DynamicCoefficient == nil && model.DynamicCoefficient != nil {
		block.DynamicCoefficient = cloneDynamicCoefficientConfig(model.DynamicCoefficient)
	}
	model.SeqLen = 0
	model.StatTest = nil
	model.DynamicCoefficient = nil
}

func genesisPrefillSharesReady(models []*PoCModelConfig) bool {
	var total uint64
	for _, model := range models {
		coeff := model.DynamicCoefficientFor(PocScheme_POC_SCHEME_PREFILL)
		if coeff == nil {
			return false
		}
		total += uint64(coeff.TargetShareBps)
	}
	return total == 10000
}

func assignGenesisPrefillShares(models []*PoCModelConfig, targetZoneBps uint32) {
	shares := genesisTargetShares(len(models), targetZoneBps)
	for i, model := range models {
		block, ok := model.SchemeParams(PocScheme_POC_SCHEME_PREFILL)
		if !ok || block == nil {
			continue
		}
		if block.DynamicCoefficient == nil {
			block.DynamicCoefficient = &DynamicCoefficientModelConfig{
				CoeffMin:           DecimalFromFloat(1),
				CoeffMax:           DecimalFromFloat(1),
				RelativeDifficulty: DecimalFromFloat(1),
			}
		}
		block.DynamicCoefficient.TargetShareBps = shares[i]
	}
}

func genesisTargetShares(n int, targetZoneBps uint32) []uint32 {
	shares := make([]uint32, n)
	if n == 0 {
		return shares
	}
	base := 10000 / n
	rem := 10000 % n
	if base == 0 || (targetZoneBps > 0 && uint32(base) <= targetZoneBps) {
		shares[0] = 10000
		return shares
	}
	for i := range shares {
		shares[i] = uint32(base)
		if i < rem {
			shares[i]++
		}
	}
	return shares
}

func clonePoCStatTest(stat *PoCStatTestParams) *PoCStatTestParams {
	if stat == nil {
		return nil
	}
	return gogoproto.Clone(stat).(*PoCStatTestParams)
}

func cloneDynamicCoefficientConfig(config *DynamicCoefficientModelConfig) *DynamicCoefficientModelConfig {
	if config == nil {
		return nil
	}
	return gogoproto.Clone(config).(*DynamicCoefficientModelConfig)
}
