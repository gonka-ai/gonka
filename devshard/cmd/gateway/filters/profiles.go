package filters

import (
	"maps"
	"slices"
)

// ThinkingBudget is how a profile resolves thinking_token_budget; a nil budget strips the field. See README.md, "Reasoning and thinking".
type ThinkingBudget struct {
	ForceZeroBelow  uint64 `json:"force_zero_below"`
	DefaultDivisor  uint64 `json:"default_divisor"`
	AbsoluteMax     uint64 `json:"absolute_max"`
	ContentHeadroom uint64 `json:"content_headroom"`
}

// ReasoningEffortPolicy says whether reasoning_effort reaches the host and which values a model accepts; an empty Allowed accepts every value.
type ReasoningEffortPolicy struct {
	Mode    ReasoningEffortMode `json:"mode"`
	Allowed []string            `json:"allowed"`
}

// SamplingLimits narrows a model's sampling parameters inside the global bounds; nil leaves the global bound alone.
type SamplingLimits struct {
	TemperatureMax       *float64 `json:"temperature_max,omitempty"`
	TopPMax              *float64 `json:"top_p_max,omitempty"`
	MinPMax              *float64 `json:"min_p_max,omitempty"`
	RepetitionPenaltyMax *float64 `json:"repetition_penalty_max,omitempty"`
	TopKMax              *int64   `json:"top_k_max,omitempty"`
}

// Profile captures one routed model's deltas from the default pipeline; a nil *Profile is the default preset. See README.md, "Model profiles".
type Profile struct {
	ForceZeroPenalties      bool
	RejectStructuredOutputs bool
	AllowSafetyIdentifier   bool
	Thinking                ThinkingDisposition
	ThinkingTemplateKey     string
	KeepReasoningSplit      bool
	DefaultReasoningSplit   *bool
	ReasoningEffort         ReasoningEffortPolicy
	Sampling                SamplingLimits
	TemplateKwargs          TemplateKwargsPolicy
	Tools                   ToolsPolicy
	ParallelToolCalls       ParallelToolCallsPolicy
	RejectJSONSchema        bool
	ThinkingTokenBudget     *ThinkingBudget
	EmptyAnswerCountsTokens bool
	OutputTokensFloor       uint64
}

const (
	enableThinkingKwargKey          = "enable_thinking"
	thinkingTemplateKeyUnset        = ""
	outputTokensFloorNone    uint64 = 0
)

// BuiltInProfileFor is the profile a model runs when no operator setting names it; nil is the default preset.
func BuiltInProfileFor(model string) *Profile {
	preset, bound := builtInBindings[model]
	if !bound {
		return nil
	}
	return presetProfiles[preset]
}

// BuiltInPresetFor is the preset a model's built-in binding names.
func BuiltInPresetFor(model string) (PresetName, bool) {
	preset, bound := builtInBindings[model]
	return preset, bound
}

// BuiltInModels lists every model a preset file binds, sorted.
func BuiltInModels() []string {
	return slices.Sorted(maps.Keys(builtInBindings))
}

// Knobs is the profile written as the filters object a PUT carries, every knob set and nothing shared with the profile.
func (profile *Profile) Knobs() FilterKnobs {
	resolved := profile.resolved().clone()
	return FilterKnobs{
		Thinking:                &resolved.Thinking,
		ThinkingTemplateKey:     &resolved.ThinkingTemplateKey,
		ThinkingTokenBudget:     resolved.ThinkingTokenBudget,
		EmptyAnswerCountsTokens: &resolved.EmptyAnswerCountsTokens,
		ForceZeroPenalties:      &resolved.ForceZeroPenalties,
		RejectStructuredOutputs: &resolved.RejectStructuredOutputs,
		AllowSafetyIdentifier:   &resolved.AllowSafetyIdentifier,
		KeepReasoningSplit:      &resolved.KeepReasoningSplit,
		DefaultReasoningSplit:   resolved.DefaultReasoningSplit,
		ReasoningEffort:         &resolved.ReasoningEffort,
		Sampling:                &resolved.Sampling,
		TemplateKwargs:          &resolved.TemplateKwargs,
		Tools:                   &resolved.Tools,
		ParallelToolCalls:       &resolved.ParallelToolCalls,
		RejectJSONSchema:        &resolved.RejectJSONSchema,
		OutputTokensFloor:       &resolved.OutputTokensFloor,
	}
}

// RequiresRegisteredModel reports the knobs only safe on a model the chain registers. See README.md, "Knobs".
func (profile *Profile) RequiresRegisteredModel() bool {
	resolved := profile.resolved()
	return resolved.ThinkingTokenBudget != nil || resolved.EmptyAnswerCountsTokens
}

// AddsThinkingBudgetTo reports a budget on a model whose built-in preset has none, the case that can crash its engine.
func (profile *Profile) AddsThinkingBudgetTo(model string) bool {
	return profile.resolved().ThinkingTokenBudget != nil && BuiltInProfileFor(model).resolved().ThinkingTokenBudget == nil
}

func (profile *Profile) resolved() *Profile {
	if profile == nil {
		return defaultProfile
	}
	return profile
}

func (profile *Profile) clone() Profile {
	copied := *profile
	copied.DefaultReasoningSplit = clonePointer(profile.DefaultReasoningSplit)
	copied.ThinkingTokenBudget = clonePointer(profile.ThinkingTokenBudget)
	copied.ReasoningEffort.Allowed = slices.Clone(profile.ReasoningEffort.Allowed)
	copied.Sampling = profile.Sampling.clone()
	copied.TemplateKwargs = profile.TemplateKwargs.clone()
	return copied
}

func (profile *Profile) stripsThinking() bool {
	return profile.resolved().Thinking == ThinkingStrip
}

func (profile *Profile) thinkingDisposition() ThinkingDisposition {
	return profile.resolved().Thinking
}

func (profile *Profile) forcesZeroPenalties() bool {
	return profile.resolved().ForceZeroPenalties
}

func (profile *Profile) allowsSafetyIdentifier() bool {
	return profile.resolved().AllowSafetyIdentifier
}

func (profile *Profile) keepsReasoningSplit() bool {
	return profile.resolved().KeepReasoningSplit
}

func (profile *Profile) defaultReasoningSplit() *bool {
	return profile.resolved().DefaultReasoningSplit
}

func (profile *Profile) rejectsStructuredOutputs() bool {
	return profile.resolved().RejectStructuredOutputs
}

func (profile *Profile) outputTokensFloor() uint64 {
	return profile.resolved().OutputTokensFloor
}

func (profile *Profile) thinkingBudget() *ThinkingBudget {
	return profile.resolved().ThinkingTokenBudget
}

func (profile *Profile) emptyAnswerCountsTokens() bool {
	return profile.resolved().EmptyAnswerCountsTokens
}

func (profile *Profile) thinkingTemplateKey() string {
	return profile.resolved().ThinkingTemplateKey
}

func (profile *Profile) reasoningEffortPolicy() ReasoningEffortPolicy {
	return profile.resolved().ReasoningEffort
}

func (profile *Profile) samplingLimits() SamplingLimits {
	return profile.resolved().Sampling
}

func (profile *Profile) templateKwargsPolicy() TemplateKwargsPolicy {
	return profile.resolved().TemplateKwargs
}

func (profile *Profile) rejectsTools() bool {
	return profile.resolved().Tools == ToolsReject
}

func (profile *Profile) stripsParallelToolCalls() bool {
	return profile.resolved().ParallelToolCalls == ParallelToolCallsStrip
}

func (profile *Profile) rejectsJSONSchema() bool {
	return profile.resolved().RejectJSONSchema
}
