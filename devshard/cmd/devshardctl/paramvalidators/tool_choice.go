package paramvalidators

import (
	"errors"
	"fmt"
)

var (
	ErrToolChoiceShape         = errors.New("tool_choice: invalid value")
	ErrToolChoiceFunctionShape = errors.New("tool_choice.function: invalid shape")
)

// ToolChoiceValidator: "auto" | "none" | {"type":"function","function":{"name":...}}.
// "required" is coerced upstream by ToolsValidator; this validator never sees it.
type ToolChoiceValidator struct {
	MaxNameLen int
}

func (v ToolChoiceValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["tool_choice"]
	if !exists {
		return nil
	}
	switch typed := raw.(type) {
	case string:
		if typed != "auto" && typed != "none" {
			return fmt.Errorf("%w: must be \"auto\", \"none\", or a function object", ErrToolChoiceShape)
		}
		return nil
	case map[string]any:
		tcType, _ := typed["type"].(string)
		if tcType != "function" {
			return fmt.Errorf("%w: type must be \"function\"", ErrToolChoiceFunctionShape)
		}
		fn, ok := typed["function"].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: function must be an object", ErrToolChoiceFunctionShape)
		}
		name, ok := fn["name"].(string)
		if !ok || name == "" {
			return fmt.Errorf("%w: function.name must be a non-empty string", ErrToolChoiceFunctionShape)
		}
		if v.MaxNameLen > 0 && len(name) > v.MaxNameLen {
			return fmt.Errorf("%w: function.name length %d exceeds limit %d", ErrToolChoiceFunctionShape, len(name), v.MaxNameLen)
		}
		return nil
	default:
		return fmt.Errorf("%w: must be \"auto\", \"none\", or a function object", ErrToolChoiceShape)
	}
}
