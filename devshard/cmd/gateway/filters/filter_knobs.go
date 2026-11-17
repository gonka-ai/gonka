package filters

import (
	"fmt"
	"math"
	"slices"
)

const (
	thinkingContentHeadroomMax = RequestMaxTokensCap
	samplingBoundIncluded      = true
	samplingBoundExcluded      = false
)

// FilterKnobs are the filters a preset file or an operator sets; nil inherits from the base. See README.md, "Knobs".
type FilterKnobs struct {
	Thinking                *ThinkingDisposition     `json:"thinking,omitempty"`
	ThinkingTemplateKey     *string                  `json:"thinking_template_key,omitempty"`
	ThinkingTokenBudget     *ThinkingBudget          `json:"thinking_token_budget,omitempty"`
	EmptyAnswerCountsTokens *bool                    `json:"empty_answer_counts_tokens,omitempty"`
	ForceZeroPenalties      *bool                    `json:"force_zero_penalties,omitempty"`
	RejectStructuredOutputs *bool                    `json:"reject_structured_outputs,omitempty"`
	AllowSafetyIdentifier   *bool                    `json:"allow_safety_identifier,omitempty"`
	KeepReasoningSplit      *bool                    `json:"keep_reasoning_split,omitempty"`
	DefaultReasoningSplit   *bool                    `json:"default_reasoning_split,omitempty"`
	ReasoningEffort         *ReasoningEffortPolicy   `json:"reasoning_effort,omitempty"`
	Sampling                *SamplingLimits          `json:"sampling,omitempty"`
	TemplateKwargs          *TemplateKwargsPolicy    `json:"template_kwargs,omitempty"`
	Tools                   *ToolsPolicy             `json:"tools,omitempty"`
	ParallelToolCalls       *ParallelToolCallsPolicy `json:"parallel_tool_calls,omitempty"`
	RejectJSONSchema        *bool                    `json:"reject_json_schema,omitempty"`
	OutputTokensFloor       *uint64                  `json:"output_tokens_floor,omitempty"`
}

func composeOver(base Profile, knobs FilterKnobs) (*Profile, error) {
	profile := base.clone()
	knobs.applyTo(&profile)
	if err := profile.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidModelSetting, err)
	}
	return &profile, nil
}

func (knobs FilterKnobs) applyTo(profile *Profile) {
	setIfPresent(&profile.Thinking, knobs.Thinking)
	setIfPresent(&profile.ThinkingTemplateKey, knobs.ThinkingTemplateKey)
	setIfPresent(&profile.EmptyAnswerCountsTokens, knobs.EmptyAnswerCountsTokens)
	setIfPresent(&profile.ForceZeroPenalties, knobs.ForceZeroPenalties)
	setIfPresent(&profile.RejectStructuredOutputs, knobs.RejectStructuredOutputs)
	setIfPresent(&profile.AllowSafetyIdentifier, knobs.AllowSafetyIdentifier)
	setIfPresent(&profile.KeepReasoningSplit, knobs.KeepReasoningSplit)
	setIfPresent(&profile.OutputTokensFloor, knobs.OutputTokensFloor)
	setIfPresent(&profile.Tools, knobs.Tools)
	setIfPresent(&profile.ParallelToolCalls, knobs.ParallelToolCalls)
	setIfPresent(&profile.RejectJSONSchema, knobs.RejectJSONSchema)
	if knobs.DefaultReasoningSplit != nil {
		profile.DefaultReasoningSplit = clonePointer(knobs.DefaultReasoningSplit)
	}
	if knobs.ReasoningEffort != nil {
		profile.ReasoningEffort = profile.ReasoningEffort.mergedWith(*knobs.ReasoningEffort)
	}
	if knobs.Sampling != nil {
		profile.Sampling = knobs.Sampling.clone()
	}
	if knobs.TemplateKwargs != nil {
		profile.TemplateKwargs = knobs.TemplateKwargs.clone()
	}
	if knobs.ThinkingTokenBudget != nil {
		profile.ThinkingTokenBudget = clonePointer(knobs.ThinkingTokenBudget)
	}
}

func setIfPresent[Value any](target *Value, knob *Value) {
	if knob != nil {
		*target = *knob
	}
}

func clonePointer[Value any](source *Value) *Value {
	if source == nil {
		return nil
	}
	copied := *source
	return &copied
}

func (profile *Profile) validate() error {
	if profile.OutputTokensFloor > MaxOutputTokensFloor {
		return fmt.Errorf("output_tokens_floor %d is above %d", profile.OutputTokensFloor, MaxOutputTokensFloor)
	}
	if err := validTemplateKey("thinking_template_key", profile.ThinkingTemplateKey); err != nil {
		return err
	}
	if profile.DefaultReasoningSplit != nil && !profile.KeepReasoningSplit {
		return fmt.Errorf("default_reasoning_split needs keep_reasoning_split: the field is stripped otherwise")
	}
	if profile.Tools != ToolsAllow && profile.Tools != ToolsReject {
		return fmt.Errorf("tools %q is not %s or %s", profile.Tools, ToolsAllow, ToolsReject)
	}
	if profile.ParallelToolCalls != ParallelToolCallsKeep && profile.ParallelToolCalls != ParallelToolCallsStrip {
		return fmt.Errorf("parallel_tool_calls %q is not %s or %s", profile.ParallelToolCalls, ParallelToolCallsKeep, ParallelToolCallsStrip)
	}
	if profile.Thinking == ThinkingForceOn && profile.TemplateKwargs.touches(profile.ThinkingTemplateKey) {
		return fmt.Errorf("thinking force_on cannot go with template_kwargs on %q: the kwargs policy would undo it", profile.ThinkingTemplateKey)
	}
	for _, check := range []func() error{profile.ReasoningEffort.validate, profile.Sampling.validate, profile.TemplateKwargs.validate, profile.validateBudget} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (profile *Profile) validateBudget() error {
	budget := profile.ThinkingTokenBudget
	if budget == nil {
		return nil
	}
	switch {
	case budget.DefaultDivisor == 0:
		return fmt.Errorf("thinking_token_budget.default_divisor must be at least 1")
	case budget.AbsoluteMax > MaxOutputTokens, budget.ForceZeroBelow > MaxOutputTokens:
		return fmt.Errorf("thinking_token_budget limits must not exceed %d", MaxOutputTokens)
	case budget.ContentHeadroom > thinkingContentHeadroomMax:
		return fmt.Errorf("thinking_token_budget.content_headroom must not exceed %d", thinkingContentHeadroomMax)
	case profile.Thinking == ThinkingForceOn:
		return fmt.Errorf("thinking_token_budget cannot go with thinking force_on: force_on would undo the silence below force_zero_below")
	case profile.TemplateKwargs.touches(profile.ThinkingTemplateKey):
		return fmt.Errorf("thinking_token_budget cannot go with template_kwargs on %q: the kwargs policy would undo the silence below force_zero_below", profile.ThinkingTemplateKey)
	}
	return nil
}

func (policy ReasoningEffortPolicy) mergedWith(knob ReasoningEffortPolicy) ReasoningEffortPolicy {
	merged := ReasoningEffortPolicy{Mode: policy.Mode, Allowed: slices.Clone(policy.Allowed)}
	if knob.Mode != reasoningEffortModeUnset {
		merged.Mode = knob.Mode
	}
	if knob.Allowed != nil {
		merged.Allowed = slices.Clone(knob.Allowed)
	}
	return merged
}

func (policy ReasoningEffortPolicy) validate() error {
	if policy.Mode != ReasoningEffortForward && policy.Mode != ReasoningEffortStrip {
		return fmt.Errorf("reasoning_effort.mode %q is not %s or %s", policy.Mode, ReasoningEffortForward, ReasoningEffortStrip)
	}
	for _, value := range policy.Allowed {
		if _, known := allowedReasoningEffortValues[value]; !known {
			return fmt.Errorf("reasoning_effort.allowed: %q is not a reasoning_effort value", value)
		}
	}
	return nil
}

func validTemplateKey(field, key string) error {
	if key == thinkingTemplateKeyUnset || len(key) > templateKwargsKeyMaxLen {
		return fmt.Errorf("%s must be 1 to %d bytes", field, templateKwargsKeyMaxLen)
	}
	if _, forbidden := forbiddenChatTemplateKwargsKeys[key]; forbidden {
		return fmt.Errorf("%s %q is a forbidden chat_template_kwargs key", field, key)
	}
	return nil
}

func (limits SamplingLimits) clone() SamplingLimits {
	return SamplingLimits{
		TemperatureMax:       clonePointer(limits.TemperatureMax),
		TopPMax:              clonePointer(limits.TopPMax),
		MinPMax:              clonePointer(limits.MinPMax),
		RepetitionPenaltyMax: clonePointer(limits.RepetitionPenaltyMax),
		TopKMax:              clonePointer(limits.TopKMax),
	}
}

type samplingBound struct {
	name           string
	limit          *float64
	lowest         float64
	lowestIncluded bool
	highest        float64
}

func (limits SamplingLimits) validate() error {
	bounds := []samplingBound{
		{name: "sampling.temperature_max", limit: limits.TemperatureMax, lowest: minTemperature, lowestIncluded: samplingBoundExcluded, highest: maxTemperature},
		{name: "sampling.top_p_max", limit: limits.TopPMax, lowest: topPExclusiveMin, lowestIncluded: samplingBoundExcluded, highest: topPMax},
		{name: "sampling.min_p_max", limit: limits.MinPMax, lowest: minPMin, lowestIncluded: samplingBoundIncluded, highest: minPMax},
		{name: "sampling.repetition_penalty_max", limit: limits.RepetitionPenaltyMax, lowest: repetitionPenaltyExclusiveMin, lowestIncluded: samplingBoundExcluded, highest: maxRepetitionPenalty},
	}
	for _, bound := range bounds {
		if bound.limit == nil {
			continue
		}
		value := *bound.limit
		tooLow := value < bound.lowest || (!bound.lowestIncluded && value == bound.lowest)
		if tooLow || value > bound.highest || math.IsNaN(value) {
			return fmt.Errorf("%s %v must be inside the global bound up to %v", bound.name, value, bound.highest)
		}
	}
	if limits.TopKMax != nil && (*limits.TopKMax < topKMin || *limits.TopKMax > topKMax) {
		return fmt.Errorf("sampling.top_k_max %d must be between %d and %d", *limits.TopKMax, topKMin, topKMax)
	}
	return nil
}
