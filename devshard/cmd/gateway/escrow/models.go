// Package escrow manages devshard escrow rotation, settlement, and crash-recovery bookkeeping.
package escrow

import (
	"fmt"
	"math"
	"strings"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// json tags are the DEVSHARD_ESCROW_ROTATION_MODELS_JSON wire contract, not renameable.
type ModelConfig struct {
	ModelID                        string `json:"model_id"`
	TempCount                      int    `json:"temp_count"`
	TargetCount                    int    `json:"target_count"`
	ReserveCount                   int    `json:"reserve_count"`
	Amount                         uint64 `json:"amount"`
	PrivateKeyEnv                  string `json:"private_key_env"`
	SettlementEnabled              *bool  `json:"settlement_enabled,omitempty"`
	PreviousEpochSettlementEnabled *bool  `json:"previous_epoch_settlement_enabled,omitempty"`
	MaxUnsettled                   int    `json:"max_unsettled"`
	FullContextSlots               int    `json:"full_context_slots"`
}

// The count defaults apply only to an absent field: an explicit target_count of 0 is rejected, an explicit reserve_count of 0 turns reserves off.
const (
	defaultTargetCount  = 1
	defaultReserveCount = 1

	defaultFullContextSlots = 2
	unsettledHeadroom       = 4
	unsetMaxUnsettled       = math.MinInt
)

func parseModels(raw string) ([]ModelConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse rotation models: %w", err)
	}
	models := make([]ModelConfig, 0, len(entries))
	for _, entry := range entries {
		model := ModelConfig{TargetCount: defaultTargetCount, ReserveCount: defaultReserveCount, MaxUnsettled: unsetMaxUnsettled, FullContextSlots: defaultFullContextSlots}
		if err := json.Unmarshal(entry, &model); err != nil {
			return nil, fmt.Errorf("parse rotation models: %w", err)
		}
		if model.MaxUnsettled == unsetMaxUnsettled {
			model.MaxUnsettled = model.TargetCount + model.TempCount + model.ReserveCount + unsettledHeadroom
		}
		models = append(models, model)
	}
	for _, model := range models {
		if model.ModelID == "" {
			return nil, fmt.Errorf("rotation model %q: model_id must not be empty", model.ModelID)
		}
		if model.TargetCount < 1 {
			return nil, fmt.Errorf("rotation model %q: target_count must be >= 1", model.ModelID)
		}
		if model.ReserveCount < 0 {
			return nil, fmt.Errorf("rotation model %q: reserve_count must be >= 0", model.ModelID)
		}
		if model.Amount == 0 {
			return nil, fmt.Errorf("rotation model %q: amount must be > 0", model.ModelID)
		}
		if model.FullContextSlots < 1 {
			return nil, fmt.Errorf("rotation model %q: full_context_slots must be >= 1", model.ModelID)
		}
		if floor := model.TargetCount + model.TempCount + model.ReserveCount + 1; model.MaxUnsettled < floor {
			return nil, fmt.Errorf("rotation model %q: max_unsettled %d must be >= target_count + temp_count + reserve_count + 1 (%d)", model.ModelID, model.MaxUnsettled, floor)
		}
	}
	return models, nil
}

// settlementPolicy lets a model's own settlement flags win over the global toggles. See README.md, "Settlement and retirement".
type settlementPolicy struct {
	anyEpoch      modelToggle
	previousEpoch modelToggle
}

type modelToggle struct {
	global  bool
	byModel map[string]bool
}

// newSettlementPolicy reads the models list whatever the rotation toggle says; a list that does not parse leaves every model on the global toggles.
func newSettlementPolicy(rotation config.Rotation) settlementPolicy {
	policy := settlementPolicy{
		anyEpoch:      modelToggle{global: rotation.SettlementEnabled, byModel: map[string]bool{}},
		previousEpoch: modelToggle{global: rotation.PreviousEpochSettlementEnabled, byModel: map[string]bool{}},
	}
	models, err := parseModels(rotation.ModelsJSON)
	if err != nil {
		return policy
	}
	for _, model := range models {
		if model.SettlementEnabled != nil {
			policy.anyEpoch.byModel[model.ModelID] = *model.SettlementEnabled
		}
		if model.PreviousEpochSettlementEnabled != nil {
			policy.previousEpoch.byModel[model.ModelID] = *model.PreviousEpochSettlementEnabled
		}
	}
	return policy
}

func (p settlementPolicy) settlesAtRetirement(modelID string) bool {
	return p.anyEpoch.enabled(modelID)
}

func (p settlementPolicy) settles(modelID string, ownEpochOver bool) bool {
	return p.anyEpoch.enabled(modelID) || (p.previousEpoch.enabled(modelID) && ownEpochOver)
}

func (toggle modelToggle) enabled(modelID string) bool {
	if enabled, set := toggle.byModel[modelID]; set {
		return enabled
	}
	return toggle.global
}

// known is false only when both weight-by-model maps are empty (cold start): callers must not skip an unknown model.
func servedByNetwork(snapshot chain.PhaseSnapshot, modelID string) (served, known bool) {
	for id := range snapshot.FullWeightsByModel {
		known = true
		if id == modelID {
			served = true
		}
	}
	for id := range snapshot.CurrentWeightsByModel {
		known = true
		if id == modelID {
			served = true
		}
	}
	return served, known
}
