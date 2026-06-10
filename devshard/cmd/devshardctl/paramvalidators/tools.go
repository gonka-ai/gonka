package paramvalidators

import (
	"errors"
	"fmt"
)

// Tool-specific sentinels. Schema-walk rejections (depth/nodes/ref/enum/branch/size) flow
// through as the shared ErrSchema* values wrapped with a "tools[i].function.parameters:"
// prefix.
var (
	ErrToolsShape        = errors.New("tools: invalid array shape")
	ErrToolShape         = errors.New("tools[i]: invalid tool shape")
	ErrToolFunctionType  = errors.New("tools[i].type: must be \"function\"")
	ErrToolFunctionShape = errors.New("tools[i].function: invalid wrapper shape")
	ErrToolFunctionName  = errors.New("tools[i].function.name: must be a non-empty string")
)

// ToolsValidator: OpenAI tool contract + cross-field tool_choice cleanup. Each tool must
// declare `type: "function"` and `function.name`; `function.parameters` is bounded via
// SchemaBounds. Also handles tool_choice defaulting: drops both fields when tools=[],
// writes DefaultToolChoice when tool_choice is absent. tool_choice == "required" is coerced
// to DefaultToolChoice (network policy: "required" is temporarily disabled -- restore here
// when re-enabling). Per-model overrides (e.g. Kimi K2.6 → "none") are wired in the catalog
// via ModelScopedParameterHandler, not here, so this validator stays model-agnostic.
type ToolsValidator struct {
	MaxDepth      int
	MaxSize       int
	MaxNodes      int
	MaxBranch     int
	MaxEnum       int
	MaxPatternLen int

	DefaultToolChoice string
}

func (v ToolsValidator) Validate(vctx ValidatorContext) error {
	// "required" temporarily disabled: collapse to the default so the downstream behavior
	// matches the no-tool_choice case rather than 400-ing the client.
	if vctx.Document["tool_choice"] == "required" {
		vctx.Document["tool_choice"] = v.DefaultToolChoice
	}
	raw, exists := vctx.Document["tools"]
	if !exists {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%w: must be an array", ErrToolsShape)
	}
	if len(arr) == 0 {
		delete(vctx.Document, "tools")
		delete(vctx.Document, "tool_choice")
		return nil
	}
	if _, hasChoice := vctx.Document["tool_choice"]; !hasChoice {
		if v.DefaultToolChoice != "" {
			vctx.Document["tool_choice"] = v.DefaultToolChoice
		}
	}
	bounds := SchemaBounds{
		MaxDepth:      v.MaxDepth,
		MaxSize:       v.MaxSize,
		MaxNodes:      v.MaxNodes,
		MaxBranch:     v.MaxBranch,
		MaxEnum:       v.MaxEnum,
		MaxPatternLen: v.MaxPatternLen,
	}
	for i, item := range arr {
		tool, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: tools[%d] must be an object", ErrToolShape, i)
		}
		toolType, ok := tool["type"].(string)
		if !ok || toolType != "function" {
			return fmt.Errorf("%w (tools[%d])", ErrToolFunctionType, i)
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: tools[%d].function must be an object", ErrToolFunctionShape, i)
		}
		name, ok := fn["name"].(string)
		if !ok || name == "" {
			return fmt.Errorf("%w (tools[%d])", ErrToolFunctionName, i)
		}
		// OpenAI Structured Outputs flag. vLLM accepts but does not honor it
		// (kimi_k2 / hermes tool parsers ignore the field; grammar enforcement
		// flows through tool_choice="required" instead). Strip silently so
		// well-behaved OpenAI clients (LangChain, etc.) keep working without
		// implying schema enforcement we can't deliver.
		delete(fn, "strict")
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			continue
		}
		if err := bounds.Walk(params); err != nil {
			return fmt.Errorf("tools[%d].function.parameters: %w", i, err)
		}
		if err := bounds.CheckSize(params); err != nil {
			return fmt.Errorf("tools[%d].function.parameters: %w", i, err)
		}
	}
	return nil
}
