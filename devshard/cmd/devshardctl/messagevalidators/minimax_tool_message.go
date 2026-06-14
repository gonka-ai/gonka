package messagevalidators

import (
	"errors"
	"fmt"
	"strings"
)

// MiniMax-M2.7 tool result messages carry per-call results as an array of
// {name, type:"text", text} objects instead of OpenAI's single content string
// keyed by tool_call_id.
var (
	ErrMinimaxToolContentShape = errors.New("content: must be a non-empty array of {name,type,text} objects")
	ErrMinimaxToolEntryShape   = errors.New("content entry: invalid shape")
)

type MinimaxToolMessage struct {
	MaxEntries  int
	NameMaxLen  int
	TextMaxSize int
}

// Validate enforces the MiniMax tool message content shape and per-entry caps.
func (v MinimaxToolMessage) Validate(content any) error {
	entries, ok := content.([]any)
	if !ok {
		return fmt.Errorf("%w: not an array", ErrMinimaxToolContentShape)
	}
	if len(entries) == 0 {
		return fmt.Errorf("%w: array is empty", ErrMinimaxToolContentShape)
	}
	if v.MaxEntries > 0 && len(entries) > v.MaxEntries {
		return fmt.Errorf("%w: %d entries exceeds limit %d", ErrMinimaxToolContentShape, len(entries), v.MaxEntries)
	}
	for i, rawEntry := range entries {
		if err := v.validateEntry(i, rawEntry); err != nil {
			return err
		}
	}
	return nil
}

func (v MinimaxToolMessage) validateEntry(index int, raw any) error {
	entry, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: [%d] must be an object", ErrMinimaxToolEntryShape, index)
	}
	// Closed allow-list. Stray keys would otherwise reach the upstream parser
	// whose union-with-null typing has been a crash vector (SGLang #16057).
	for key := range entry {
		switch key {
		case "name", "type", "text":
		default:
			return fmt.Errorf("%w: [%d] has unsupported key %q", ErrMinimaxToolEntryShape, index, key)
		}
	}
	name, ok := entry["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: [%d].name must be a non-empty string", ErrMinimaxToolEntryShape, index)
	}
	if v.NameMaxLen > 0 && len(name) > v.NameMaxLen {
		return fmt.Errorf("%w: [%d].name length %d exceeds limit %d", ErrMinimaxToolEntryShape, index, len(name), v.NameMaxLen)
	}
	partType, ok := entry["type"].(string)
	if !ok || partType != "text" {
		return fmt.Errorf("%w: [%d].type must be \"text\"", ErrMinimaxToolEntryShape, index)
	}
	text, ok := entry["text"].(string)
	if !ok {
		return fmt.Errorf("%w: [%d].text must be a string", ErrMinimaxToolEntryShape, index)
	}
	if v.TextMaxSize > 0 && len(text) > v.TextMaxSize {
		return fmt.Errorf("%w: [%d].text size %d exceeds limit %d", ErrMinimaxToolEntryShape, index, len(text), v.TextMaxSize)
	}
	return nil
}
