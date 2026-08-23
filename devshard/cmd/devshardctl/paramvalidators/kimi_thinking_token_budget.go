package paramvalidators

// KimiThinkingTokenBudgetValidator resolves thinking_token_budget for Kimi-K2.6:
// force 0 at small max_tokens, else default to max_tokens/Divisor, cap at
// AbsoluteMax, clamp to (max_tokens - ContentHeadroom). No-op for other models.
type KimiThinkingTokenBudgetValidator struct {
	Model                   string
	DefaultDivisor          uint64
	AbsoluteMax             uint64
	ContentHeadroom         uint64
	ForceZeroBelowMaxTokens uint64
}

func (v KimiThinkingTokenBudgetValidator) Validate(vctx ValidatorContext) error {
	if vctx.RoutedModel != v.Model {
		return nil
	}
	maxTokens, ok := numericAsUint64(vctx.Document["max_tokens"])
	if !ok || maxTokens == 0 {
		return nil
	}
	if v.ForceZeroBelowMaxTokens > 0 && maxTokens < v.ForceZeroBelowMaxTokens {
		vctx.Document["thinking_token_budget"] = uint64(0)
		return nil
	}
	if _, exists := vctx.Document["thinking_token_budget"]; !exists && v.DefaultDivisor > 0 {
		vctx.Document["thinking_token_budget"] = maxTokens / v.DefaultDivisor
	}
	ttb, ok := numericAsUint64(vctx.Document["thinking_token_budget"])
	if !ok {
		return nil
	}
	if v.AbsoluteMax > 0 && ttb > v.AbsoluteMax {
		ttb = v.AbsoluteMax
	}
	var headroomCap uint64
	if maxTokens > v.ContentHeadroom {
		headroomCap = maxTokens - v.ContentHeadroom
	}
	if ttb > headroomCap {
		ttb = headroomCap
	}
	vctx.Document["thinking_token_budget"] = ttb
	return nil
}
