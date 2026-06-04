package paramvalidators

import (
	"errors"
	"fmt"
)

var (
	ErrReasoningEffortShape = errors.New("reasoning_effort: invalid shape")
	ErrReasoningEffortValue = errors.New("reasoning_effort: unsupported value")
)

// Re-check the strip wiring in the catalog whenever a reasoning-capable model is
// added to devshard: today every routed model is non-reasoning so the catalog
// strips reasoning_effort for all of them via ModelScopedParameterHandler{Models: nil}.
type ReasoningEffortValidator struct{}

var allowedReasoningEffortValues = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
}

func (v ReasoningEffortValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["reasoning_effort"]
	if !exists {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%w: must be a string", ErrReasoningEffortShape)
	}
	if _, ok := allowedReasoningEffortValues[s]; !ok {
		return fmt.Errorf("%w: got %q", ErrReasoningEffortValue, s)
	}
	return nil
}
