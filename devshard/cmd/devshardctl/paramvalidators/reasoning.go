package paramvalidators

type ReasoningValidator struct{}

func (v ReasoningValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["reasoning"]
	if !exists {
		return nil
	}
	delete(vctx.Document, "reasoning")

	inner, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if enabled, ok := inner["enabled"].(bool); ok && !enabled {
		return nil
	}
	effort, hasEffort := inner["effort"]
	if !hasEffort {
		return nil
	}
	if _, alreadyTop := vctx.Document["reasoning_effort"]; alreadyTop {
		return nil
	}
	vctx.Document["reasoning_effort"] = effort
	return nil
}
