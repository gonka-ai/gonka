package completionapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	roleDeveloper = "developer"
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleFunction  = "function"
)

// ValidateOpenAICompatRequestBody validates OpenAI-compatible chat message schema.
// If "messages" is absent, validation is skipped so legacy/completions payloads stay compatible.
func ValidateOpenAICompatRequestBody(requestBytes []byte) error {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return err
	}
	return validateOpenAICompatRequestMap(requestMap)
}

func validateOpenAICompatRequestMap(requestMap map[string]interface{}) error {
	rawMessages, hasMessages := requestMap["messages"]
	if !hasMessages {
		return nil
	}

	messages, ok := rawMessages.([]interface{})
	if !ok {
		return fmt.Errorf("messages must be an array")
	}

	pendingToolCalls := map[string]struct{}{}
	for i, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			return fmt.Errorf("messages[%d] must be an object", i)
		}

		role, err := getRequiredNonEmptyString(message, "role")
		if err != nil {
			return fmt.Errorf("messages[%d].role: %w", i, err)
		}

		switch role {
		case roleDeveloper, roleSystem, roleUser:
			if err := ensureFieldsAbsent(message, "tool_calls", "tool_call_id", "function_call"); err != nil {
				return fmt.Errorf("messages[%d]: %w", i, err)
			}
			if err := validateRequiredContent(message); err != nil {
				return fmt.Errorf("messages[%d].content: %w", i, err)
			}
		case roleAssistant:
			if err := ensureFieldsAbsent(message, "tool_call_id"); err != nil {
				return fmt.Errorf("messages[%d]: %w", i, err)
			}

			toolCallIDs, hasToolCalls, err := validateToolCallsField(message, i)
			if err != nil {
				return err
			}
			hasFunctionCall, err := validateFunctionCallField(message, i)
			if err != nil {
				return err
			}
			if err := validateAssistantContent(message, hasToolCalls || hasFunctionCall); err != nil {
				return fmt.Errorf("messages[%d].content: %w", i, err)
			}

			for _, id := range toolCallIDs {
				pendingToolCalls[id] = struct{}{}
			}
		case roleTool:
			if err := ensureFieldsAbsent(message, "tool_calls", "function_call", "name"); err != nil {
				return fmt.Errorf("messages[%d]: %w", i, err)
			}
			toolCallID, err := getRequiredNonEmptyString(message, "tool_call_id")
			if err != nil {
				return fmt.Errorf("messages[%d].tool_call_id: %w", i, err)
			}
			if _, ok := pendingToolCalls[toolCallID]; !ok {
				return fmt.Errorf("messages[%d].tool_call_id does not match any previous assistant tool_calls", i)
			}
			delete(pendingToolCalls, toolCallID)
			if err := validateRequiredContent(message); err != nil {
				return fmt.Errorf("messages[%d].content: %w", i, err)
			}
		case roleFunction:
			if err := ensureFieldsAbsent(message, "tool_calls", "tool_call_id", "function_call"); err != nil {
				return fmt.Errorf("messages[%d]: %w", i, err)
			}
			if _, err := getRequiredNonEmptyString(message, "name"); err != nil {
				return fmt.Errorf("messages[%d].name: %w", i, err)
			}
			if err := validateRequiredContent(message); err != nil {
				return fmt.Errorf("messages[%d].content: %w", i, err)
			}
		default:
			return fmt.Errorf("messages[%d].role has unsupported value %q", i, role)
		}
	}

	return nil
}

func validateToolCallsField(message map[string]interface{}, messageIndex int) ([]string, bool, error) {
	rawToolCalls, exists := message["tool_calls"]
	if !exists {
		return nil, false, nil
	}

	toolCalls, ok := rawToolCalls.([]interface{})
	if !ok {
		return nil, true, fmt.Errorf("messages[%d].tool_calls must be an array", messageIndex)
	}
	if len(toolCalls) == 0 {
		return nil, true, fmt.Errorf("messages[%d].tool_calls must not be empty", messageIndex)
	}

	seenIDs := map[string]struct{}{}
	ids := make([]string, 0, len(toolCalls))
	for callIndex, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]interface{})
		if !ok {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d] must be an object", messageIndex, callIndex)
		}

		id, err := getRequiredNonEmptyString(call, "id")
		if err != nil {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].id: %w", messageIndex, callIndex, err)
		}
		if _, exists := seenIDs[id]; exists {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].id is duplicated", messageIndex, callIndex)
		}
		seenIDs[id] = struct{}{}

		callType, err := getRequiredNonEmptyString(call, "type")
		if err != nil {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].type: %w", messageIndex, callIndex, err)
		}
		if callType != "function" {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].type must be \"function\"", messageIndex, callIndex)
		}

		rawFunction, exists := call["function"]
		if !exists || rawFunction == nil {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].function is required", messageIndex, callIndex)
		}
		functionObj, ok := rawFunction.(map[string]interface{})
		if !ok {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].function must be an object", messageIndex, callIndex)
		}
		if _, err := getRequiredNonEmptyString(functionObj, "name"); err != nil {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].function.name: %w", messageIndex, callIndex, err)
		}
		if err := validateOptionalStringField(functionObj, "arguments"); err != nil {
			return nil, true, fmt.Errorf("messages[%d].tool_calls[%d].function.arguments: %w", messageIndex, callIndex, err)
		}

		ids = append(ids, id)
	}

	return ids, true, nil
}

func validateFunctionCallField(message map[string]interface{}, messageIndex int) (bool, error) {
	rawFunctionCall, exists := message["function_call"]
	if !exists {
		return false, nil
	}
	if rawFunctionCall == nil {
		return true, fmt.Errorf("messages[%d].function_call must be an object", messageIndex)
	}

	functionCall, ok := rawFunctionCall.(map[string]interface{})
	if !ok {
		return true, fmt.Errorf("messages[%d].function_call must be an object", messageIndex)
	}
	if _, err := getRequiredNonEmptyString(functionCall, "name"); err != nil {
		return true, fmt.Errorf("messages[%d].function_call.name: %w", messageIndex, err)
	}
	if err := validateOptionalStringField(functionCall, "arguments"); err != nil {
		return true, fmt.Errorf("messages[%d].function_call.arguments: %w", messageIndex, err)
	}

	return true, nil
}

func validateAssistantContent(message map[string]interface{}, canBeEmpty bool) error {
	content, exists := message["content"]
	if !exists || content == nil {
		if canBeEmpty {
			return nil
		}
		return fmt.Errorf("is required unless tool_calls or function_call is provided")
	}
	return validateNonEmptyContent(content)
}

func validateRequiredContent(message map[string]interface{}) error {
	content, exists := message["content"]
	if !exists || content == nil {
		return fmt.Errorf("is required")
	}
	return validateNonEmptyContent(content)
}

func validateNonEmptyContent(content interface{}) error {
	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("must not be empty")
		}
		return nil
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for i, rawPart := range v {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				return fmt.Errorf("[%d] must be an object", i)
			}
			partType, err := getRequiredNonEmptyString(part, "type")
			if err != nil {
				return fmt.Errorf("[%d].type: %w", i, err)
			}
			if partType == "text" {
				text, err := getRequiredNonEmptyString(part, "text")
				if err != nil {
					return fmt.Errorf("[%d].text: %w", i, err)
				}
				if strings.TrimSpace(text) == "" {
					return fmt.Errorf("[%d].text must not be empty", i)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string or an array of typed content parts")
	}
}

func ensureFieldsAbsent(values map[string]interface{}, fields ...string) error {
	for _, field := range fields {
		if _, exists := values[field]; exists {
			return fmt.Errorf("%s is not allowed for this role", field)
		}
	}
	return nil
}

func getRequiredNonEmptyString(values map[string]interface{}, field string) (string, error) {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return "", fmt.Errorf("is required")
	}
	stringValue, ok := rawValue.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	if strings.TrimSpace(stringValue) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return stringValue, nil
}

func validateOptionalStringField(values map[string]interface{}, field string) error {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return nil
	}
	if _, ok := rawValue.(string); !ok {
		return fmt.Errorf("must be a string")
	}
	return nil
}
