package paramvalidators

import (
	"errors"
	"fmt"
)

var (
	ErrReasoningEffortShape = errors.New("reasoning_effort: invalid shape")
	ErrReasoningEffortValue = errors.New("reasoning_effort: unsupported value")
)

type ReasoningEffortValidator struct{}

var allowedReasoningEffortValues = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {}, // DeepSeek-V4 only
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
