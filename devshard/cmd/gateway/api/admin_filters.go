package api

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
)

type filterEntry struct {
	Model     string               `json:"model"`
	Source    filterSource         `json:"source"`
	Problem   string               `json:"problem,omitempty"`
	Preset    filters.PresetName   `json:"preset"`
	Filters   *filters.FilterKnobs `json:"filters,omitempty"`
	Effective filters.FilterKnobs  `json:"effective"`
	OnChain   bool                 `json:"on_chain"`
	UpdatedAt string               `json:"updated_at,omitempty"`
	Limits    modelLimitsView      `json:"limits"`
	Warnings  []string             `json:"warnings,omitempty"`
}

type modelLimitsView struct {
	DefaultMaxTokens int64        `json:"default_max_tokens"`
	MaxTokensCap     int64        `json:"max_tokens_cap"`
	MaxModelLen      uint64       `json:"max_model_len,omitempty"`
	Source           limitsSource `json:"source"`
}

var errEmptyModelSetting = errors.New("name a preset, filters, or both")

func (s *Server) handleAdminFilters(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	snapshot := s.snapshots.Snapshot()
	records, profiles := s.modelFilters.Snapshot()
	models := map[string]bool{}
	for _, model := range filters.BuiltInModels() {
		models[model] = true
	}
	for model := range records {
		models[model] = true
	}
	for model := range snapshot.Models {
		models[model] = true
	}
	entries := make([]filterEntry, 0, len(models))
	for _, model := range slices.Sorted(maps.Keys(models)) {
		entries = append(entries, s.filterEntryFor(model, records, profiles, snapshot))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": entries})
}

func (s *Server) handleAdminFilterPresets(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": filters.PresetCatalog()})
}

func (s *Server) handleAdminModelFilter(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPut, http.MethodDelete) {
		return
	}
	model := strings.TrimSpace(r.PathValue("model"))
	if !filters.IsValidModelName(model) {
		writeError(w, http.StatusBadRequest, "model must be a non-empty name of letters, digits and ._-/")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.putModelFilter(w, r, model)
	case http.MethodDelete:
		if writeControlFailure(w, s.modelFilters.Delete(r.Context(), model)) {
			return
		}
		auditAdmin("model filters removed", "model", model)
		s.writeFilterEntry(w, model)
	default:
		s.writeFilterEntry(w, model)
	}
}

func (s *Server) putModelFilter(w http.ResponseWriter, r *http.Request, model string) {
	allowOffChain, err := allowOffChainFrom(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var setting filters.ModelSetting
	if err := decodeAdminBodyStrictly(w, r, &setting); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	if setting.IsEmpty() {
		writeError(w, http.StatusBadRequest, errEmptyModelSetting.Error())
		return
	}
	profile, err := filters.ComposeProfile(model, setting)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	needsChain := profile.RequiresRegisteredModel()
	if (!allowOffChain || needsChain) && !s.requireOnChain(w, model, needsChain) {
		return
	}
	if writeControlFailure(w, s.modelFilters.Put(r.Context(), model, setting)) {
		return
	}
	auditAdmin("model filters set", "model", model, "preset", setting.PresetFor(model), "allow_off_chain", allowOffChain,
		"engine_crash_risk", profile.AddsThinkingBudgetTo(model))
	s.writeFilterEntry(w, model)
}

func (s *Server) requireOnChain(w http.ResponseWriter, model string, knobsNeedChain bool) bool {
	remedy := fmt.Sprintf("pass %s=true to configure it anyway", allowOffChainParameter)
	if knobsNeedChain {
		remedy = "this setting's thinking budget or empty-answer exemption needs a model the chain registers"
	}
	snapshot := s.snapshots.Snapshot()
	if snapshot.LastHealthyAt.IsZero() || len(snapshot.Models) == 0 {
		writeCodedError(w, http.StatusServiceUnavailable, errorCodeChainModelsUnavailable,
			"the chain model list has not been observed yet; retry; "+remedy)
		return false
	}
	if _, onChain := snapshot.Models[model]; !onChain {
		writeCodedError(w, http.StatusNotFound, errorCodeModelNotOnChain,
			fmt.Sprintf("model %q is not registered on chain; %s", model, remedy))
		return false
	}
	return true
}

func allowOffChainFrom(r *http.Request) (bool, error) {
	raw := r.URL.Query().Get(allowOffChainParameter)
	if raw == allowOffChainAbsent {
		return false, nil
	}
	allowed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", allowOffChainParameter, raw)
	}
	return allowed, nil
}

func (s *Server) writeFilterEntry(w http.ResponseWriter, model string) {
	records, profiles := s.modelFilters.Snapshot()
	writeJSON(w, http.StatusOK, s.filterEntryFor(model, records, profiles, s.snapshots.Snapshot()))
}

func (s *Server) filterEntryFor(model string, records map[string]ModelFilterRecord, profiles *filters.ProfileSet, snapshot chain.PhaseSnapshot) filterEntry {
	entry := s.storedOrBuiltInEntry(model, records, profiles, snapshot)
	entry.Warnings = limitWarnings(entry.Effective, entry.Limits)
	return entry
}

func limitWarnings(effective filters.FilterKnobs, limits modelLimitsView) []string {
	tokenCap := uint64(max(limits.MaxTokensCap, 0))
	floor := *effective.OutputTokensFloor
	defaultBudget := max(uint64(max(limits.DefaultMaxTokens, 0)), min(floor, tokenCap))
	var warnings []string
	if floor > tokenCap {
		warnings = append(warnings, fmt.Sprintf("output_tokens_floor %d is above max_tokens_cap %d: requests get the cap, never the floor", floor, tokenCap))
	}
	if budget := effective.ThinkingTokenBudget; budget != nil {
		if budget.ForceZeroBelow > tokenCap {
			warnings = append(warnings, fmt.Sprintf("max_tokens_cap %d is below thinking_token_budget.force_zero_below %d: thinking is silenced on every request that names a budget", tokenCap, budget.ForceZeroBelow))
		}
		if budget.ForceZeroBelow > defaultBudget {
			warnings = append(warnings, fmt.Sprintf("the default output budget %d is below thinking_token_budget.force_zero_below %d: thinking is silenced on every request that names no budget", defaultBudget, budget.ForceZeroBelow))
		}
	}
	return warnings
}

func (s *Server) storedOrBuiltInEntry(model string, records map[string]ModelFilterRecord, profiles *filters.ProfileSet, snapshot chain.PhaseSnapshot) filterEntry {
	governed, onChain := snapshot.Models[model]
	entry := filterEntry{
		Model:     model,
		Source:    filterSourceDefault,
		Preset:    filters.PresetDefault,
		Effective: profiles.For(model).Knobs(),
		OnChain:   onChain,
		Limits:    s.modelLimitsFor(model, governed.MaxModelLen),
	}
	if record, stored := records[model]; stored && record.Unreadable() {
		entry.Source = filterSourceUnreadable
		entry.Preset = record.Setting.PresetFor(model)
		entry.Problem = record.Problem
		entry.UpdatedAt = record.UpdatedAt
		return entry
	}
	if record, stored := records[model]; stored {
		knobs := record.Setting.Filters
		entry.Source = filterSourceStored
		entry.Preset = record.Setting.PresetFor(model)
		entry.Filters = &knobs
		entry.UpdatedAt = record.UpdatedAt
		return entry
	}
	if preset, bound := filters.BuiltInPresetFor(model); bound {
		entry.Source = filterSourceBuiltIn
		entry.Preset = preset
	}
	return entry
}

func (s *Server) modelLimitsFor(model string, governedMaxModelLen uint64) modelLimitsView {
	configured := s.config.Load().Limits
	view := modelLimitsView{
		DefaultMaxTokens: configured.DefaultMaxTokens,
		MaxTokensCap:     configured.MaxTokensCap,
		MaxModelLen:      configured.ContextLength(model, governedMaxModelLen),
		Source:           limitsSourceGlobal,
	}
	if override, overridden := configured.ModelLimits[model]; overridden {
		view.DefaultMaxTokens, view.MaxTokensCap, view.Source = override.DefaultMaxTokens, override.MaxTokensCap, limitsSourceModel
	}
	return view
}
