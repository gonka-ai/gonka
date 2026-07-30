package messagevalidators

import "fmt"

// ValidateToolCallsField validates the message.tool_calls field and returns
// (collected ids, hasField, error). A null tool_calls is treated as absent
// and silently removed (some SDKs serialize empty slots that way). Errors
// carry no `messages[%d]` prefix — the caller wraps them.
func ValidateToolCallsField(message map[string]any) ([]string, bool, error) {
	rawToolCalls, exists := message["tool_calls"]
	if !exists {
		return nil, false, nil
	}
	if rawToolCalls == nil {
		delete(message, "tool_calls")
		return nil, false, nil
	}
	toolCalls, ok := rawToolCalls.([]any)
	if !ok {
		return nil, true, fmt.Errorf("tool_calls must be an array")
	}
	if len(toolCalls) == 0 {
		return nil, true, fmt.Errorf("tool_calls must not be empty")
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(toolCalls))
	for callIndex, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			return nil, true, fmt.Errorf("tool_calls[%d] must be an object", callIndex)
		}
		id, err := RequiredNonEmptyString(call, "id")
		if err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].id: %w", callIndex, err)
		}
		if _, exists := seen[id]; exists {
			return nil, true, fmt.Errorf("tool_calls[%d].id is duplicated", callIndex)
		}
		seen[id] = struct{}{}
		callType, err := RequiredNonEmptyString(call, "type")
		if err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].type: %w", callIndex, err)
		}
		if callType != "function" {
			return nil, true, fmt.Errorf("tool_calls[%d].type must be \"function\"", callIndex)
		}
		function, ok := call["function"].(map[string]any)
		if !ok {
			return nil, true, fmt.Errorf("tool_calls[%d].function must be an object", callIndex)
		}
		if _, err := RequiredNonEmptyString(function, "name"); err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].function.name: %w", callIndex, err)
		}
		if err := OptionalStringField(function, "arguments"); err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].function.arguments: %w", callIndex, err)
		}
		ids = append(ids, id)
	}
	return ids, true, nil
}

func ValidateFunctionCallField(message map[string]any) (bool, error) {
	rawFunctionCall, exists := message["function_call"]
	if !exists {
		return false, nil
	}
	if rawFunctionCall == nil {
		delete(message, "function_call")
		return false, nil
	}
	functionCall, ok := rawFunctionCall.(map[string]any)
	if !ok {
		return true, fmt.Errorf("function_call must be an object")
	}
	if _, err := RequiredNonEmptyString(functionCall, "name"); err != nil {
		return true, fmt.Errorf("function_call.name: %w", err)
	}
	if err := OptionalStringField(functionCall, "arguments"); err != nil {
		return true, fmt.Errorf("function_call.arguments: %w", err)
	}
	return true, nil
}
