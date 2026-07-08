package messagevalidators

import (
	"fmt"
	"strings"
)

// RequiredNonEmptyString returns the trimmed string value at values[field]
// or describes why it's missing/wrong. Returned errors carry no positional
// prefix — the caller (which knows the message/part index) wraps them.
func RequiredNonEmptyString(values map[string]any, field string) (string, error) {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
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

func OptionalStringField(values map[string]any, field string) error {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return nil
	}
	if _, ok := rawValue.(string); !ok {
		return fmt.Errorf("must be a string")
	}
	return nil
}

func EnsureFieldsAbsent(values map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, exists := values[field]; exists {
			return fmt.Errorf("%s is not allowed for this role", field)
		}
	}
	return nil
}
