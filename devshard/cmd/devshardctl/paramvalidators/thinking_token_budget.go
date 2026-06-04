package paramvalidators

// ThinkingTokenBudgetDefaultsValidator injects a default `thinking_token_budget` derived
// from `max_tokens` when the client did not supply one. Model routing is the catalog's
// concern — wrap this validator in a ModelScopedParameterHandler.
type ThinkingTokenBudgetDefaultsValidator struct {
	DefaultDivisor uint64
}

func (v ThinkingTokenBudgetDefaultsValidator) Validate(vctx ValidatorContext) error {
	if _, exists := vctx.Document["thinking_token_budget"]; exists {
		return nil
	}
	maxTokens, ok := numericAsUint64(vctx.Document["max_tokens"])
	if !ok || maxTokens == 0 {
		return nil
	}
	value := maxTokens
	if v.DefaultDivisor > 0 {
		value = maxTokens / v.DefaultDivisor
	}
	vctx.Document["thinking_token_budget"] = value
	return nil
}
