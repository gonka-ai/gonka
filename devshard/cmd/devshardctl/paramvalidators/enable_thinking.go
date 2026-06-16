package paramvalidators

import (
	"errors"
	"fmt"
)

var ErrEnableThinkingShape = errors.New("enable_thinking: must be a boolean")

type EnableThinkingValidator struct{}

func (v EnableThinkingValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["enable_thinking"]
	if !exists {
		return nil
	}
	b, ok := raw.(bool)
	if !ok {
		return fmt.Errorf("%w: got %T", ErrEnableThinkingShape, raw)
	}
	kwargs, err := getOrCreateChatTemplateKwargs(vctx.Document)
	if err != nil {
		return err
	}
	delete(vctx.Document, "enable_thinking")
	if _, exists := kwargs["enable_thinking"]; exists {
		return nil
	}
	kwargs["enable_thinking"] = b
	return nil
}
