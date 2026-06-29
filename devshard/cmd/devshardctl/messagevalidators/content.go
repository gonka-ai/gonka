package messagevalidators

import (
	"fmt"
	"strings"
)

// ValidateNonEmptyContent rejects empty strings and empty/malformed content-part
// arrays. Each non-string content must be a []any of typed text parts.
func ValidateNonEmptyContent(content any) error {
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
		for i, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return fmt.Errorf("[%d] must be an object", i)
			}
			text, err := RequiredTextContentPart(part, i)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("[%d].text must not be empty", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string or an array of typed content parts")
	}
}

func ValidateRequiredContentField(message map[string]any) error {
	content, exists := message["content"]
	if !exists || content == nil {
		return fmt.Errorf("is required")
	}
	return ValidateNonEmptyContent(content)
}

func ValidateAssistantContentField(message map[string]any, canBeEmpty bool) error {
	content, exists := message["content"]
	if !exists || content == nil {
		if canBeEmpty {
			return nil
		}
		return fmt.Errorf("is required unless tool_calls or function_call is provided")
	}
	return ValidateNonEmptyContent(content)
}

// RequiredTextContentPart validates that part has type:"text" and a non-empty text field.
// Returned errors carry no `messages[%d].content` prefix — the caller adds it.
func RequiredTextContentPart(part map[string]any, partIndex int) (string, error) {
	partType, err := RequiredNonEmptyString(part, "type")
	if err != nil {
		return "", fmt.Errorf("[%d].type: %w", partIndex, err)
	}
	if partType != "text" {
		return "", fmt.Errorf("[%d].type has unsupported value %q", partIndex, partType)
	}
	text, err := RequiredNonEmptyString(part, "text")
	if err != nil {
		return "", fmt.Errorf("[%d].text: %w", partIndex, err)
	}
	return text, nil
}

// CombineTextContentParts joins typed text parts into a single newline-separated string.
// Returned errors carry no `messages[%d].content` prefix — the caller adds it.
func CombineTextContentParts(parts []any) (string, error) {
	texts := make([]string, 0, len(parts))
	for partIndex, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return "", fmt.Errorf("[%d] must be an object", partIndex)
		}
		text, err := RequiredTextContentPart(part, partIndex)
		if err != nil {
			return "", err
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return "", nil
	}
	return strings.Join(texts, "\n"), nil
}

func IsEmptyContent(content any) bool {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

func IsAssistantTurnEmpty(msg map[string]any) bool {
	if raw, exists := msg["tool_calls"]; exists && raw != nil {
		if calls, ok := raw.([]any); ok && len(calls) > 0 {
			return false
		}
	}
	if raw, exists := msg["function_call"]; exists && raw != nil {
		if fc, ok := raw.(map[string]any); ok && len(fc) > 0 {
			return false
		}
	}
	content, exists := msg["content"]
	if !exists || content == nil {
		return true
	}
	return IsEmptyContent(content)
}
