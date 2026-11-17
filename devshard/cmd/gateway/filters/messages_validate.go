package filters

import (
	"fmt"
	"strings"
)

func validateMessages(document *Document) error {
	rawMessages, exists := document.Array("messages")
	if !exists {
		return Reject("messages is required")
	}
	if len(rawMessages) == 0 {
		return Reject("messages must not be empty")
	}
	pendingToolCalls := map[string]struct{}{}
	for index, rawMessage := range rawMessages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return Reject("messages[%d] must be an object", index)
		}
		role, err := requiredNonEmptyStringField(message, "role")
		if err != nil {
			return Reject("messages[%d].role: %v", index, err)
		}
		policy, known := messageRolePolicies[role]
		if !known {
			return Reject("messages[%d].role has unsupported value %q", index, role)
		}
		if err := ensureFieldsAbsent(message, policy.disallowedFields...); err != nil {
			return Reject("messages[%d]: %v", index, err)
		}
		if err := validateMessageRoleFields(message, index, role, policy, pendingToolCalls); err != nil {
			return err
		}
	}
	return nil
}

// validateMessageRoleFields checks one role's own fields; content is required unless assistant carries a call.
func validateMessageRoleFields(message map[string]any, index int, role string, policy messageRolePolicy, pendingToolCalls map[string]struct{}) error {
	switch role {
	case roleDeveloper, roleSystem, roleUser:
		if err := validateRequiredContentField(message); err != nil {
			return Reject("messages[%d].content: %v", index, err)
		}
	case roleAssistant:
		toolCallIDs, hasToolCalls, err := validateToolCallsField(message)
		if err != nil {
			return Reject("messages[%d].%v", index, err)
		}
		hasFunctionCall, err := validateFunctionCallField(message)
		if err != nil {
			return Reject("messages[%d].%v", index, err)
		}
		if err := validateAssistantContentField(message, hasToolCalls || hasFunctionCall); err != nil {
			return Reject("messages[%d].content: %v", index, err)
		}
		for _, id := range toolCallIDs {
			pendingToolCalls[id] = struct{}{}
		}
	case roleTool:
		if policy.requireToolCallID {
			toolCallID, err := requiredNonEmptyStringField(message, "tool_call_id")
			if err != nil {
				return Reject("messages[%d].tool_call_id: %v", index, err)
			}
			if _, pending := pendingToolCalls[toolCallID]; !pending {
				return Reject("messages[%d].tool_call_id does not match any previous assistant tool_calls", index)
			}
			delete(pendingToolCalls, toolCallID)
		}
		if err := validateRequiredContentField(message); err != nil {
			return Reject("messages[%d].content: %v", index, err)
		}
	case roleFunction:
		if policy.requireName {
			if _, err := requiredNonEmptyStringField(message, "name"); err != nil {
				return Reject("messages[%d].name: %v", index, err)
			}
		}
		if err := validateRequiredContentField(message); err != nil {
			return Reject("messages[%d].content: %v", index, err)
		}
	}
	return nil
}

// requiredNonEmptyStringField returns the trimmed-nonblank string, or why it isn't; the caller adds the positional prefix.
func requiredNonEmptyStringField(fields map[string]any, key string) (string, error) {
	rawValue, exists := fields[key]
	if !presentField(rawValue, exists) {
		return "", fmt.Errorf("is required")
	}
	value, ok := rawValue.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return value, nil
}

// optionalStringField rejects fields[key] only when present, non-null, and not a string.
func optionalStringField(fields map[string]any, key string) error {
	rawValue, exists := fields[key]
	if !presentField(rawValue, exists) {
		return nil
	}
	if _, ok := rawValue.(string); !ok {
		return fmt.Errorf("must be a string")
	}
	return nil
}

// ensureFieldsAbsent rejects the first disallowed field present; an explicit null still counts as present.
func ensureFieldsAbsent(fields map[string]any, disallowed ...string) error {
	for _, key := range disallowed {
		if _, exists := fields[key]; exists {
			return fmt.Errorf("%s is not allowed for this role", key)
		}
	}
	return nil
}

// validateNonEmptyContent accepts a non-blank string, or a non-empty array of {type:"text",text} parts.
func validateNonEmptyContent(content any) error {
	switch value := content.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("must not be empty")
		}
		return nil
	case []any:
		if len(value) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for index, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return fmt.Errorf("[%d] must be an object", index)
			}
			text, err := requiredTextContentPart(part, index)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("[%d].text must not be empty", index)
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string or an array of typed content parts")
	}
}

func validateRequiredContentField(message map[string]any) error {
	content, exists := message["content"]
	if !presentField(content, exists) {
		return fmt.Errorf("is required")
	}
	return validateNonEmptyContent(content)
}

// validateAssistantContentField allows a missing content when canBeEmpty; a present field is always shape-checked.
func validateAssistantContentField(message map[string]any, canBeEmpty bool) error {
	content, exists := message["content"]
	if !presentField(content, exists) {
		if canBeEmpty {
			return nil
		}
		return fmt.Errorf("is required unless tool_calls or function_call is provided")
	}
	return validateNonEmptyContent(content)
}

// validateToolCallsField validates tool_calls and returns its ids; a null is removed as absent (some SDKs serialize empty slots so).
func validateToolCallsField(message map[string]any) ([]string, bool, error) {
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
		id, err := requiredNonEmptyStringField(call, "id")
		if err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].id: %w", callIndex, err)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, true, fmt.Errorf("tool_calls[%d].id is duplicated", callIndex)
		}
		seen[id] = struct{}{}
		callType, err := requiredNonEmptyStringField(call, "type")
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
		if _, err := requiredNonEmptyStringField(function, "name"); err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].function.name: %w", callIndex, err)
		}
		if err := optionalStringField(function, "arguments"); err != nil {
			return nil, true, fmt.Errorf("tool_calls[%d].function.arguments: %w", callIndex, err)
		}
		ids = append(ids, id)
	}
	return ids, true, nil
}

// validateFunctionCallField validates the legacy function_call; a null value is removed as absent.
func validateFunctionCallField(message map[string]any) (bool, error) {
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
	if _, err := requiredNonEmptyStringField(functionCall, "name"); err != nil {
		return true, fmt.Errorf("function_call.name: %w", err)
	}
	if err := optionalStringField(functionCall, "arguments"); err != nil {
		return true, fmt.Errorf("function_call.arguments: %w", err)
	}
	return true, nil
}
