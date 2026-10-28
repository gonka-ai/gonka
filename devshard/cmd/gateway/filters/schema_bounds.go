package filters

import (
	"fmt"
	"regexp"

	json "github.com/goccy/go-json"
)

// Bound families kept separate per schema-carrying field even where the values match. See README.md, "Schema bounds".
const (
	chatTemplateKwargsMaxDepth     = 16
	chatTemplateKwargsMaxSizeBytes = 16 * 1024
	chatTemplateKwargsMaxNodes     = 128

	toolsMaxDepth      = 16
	toolsMaxSizeBytes  = 64 * 1024
	toolsMaxNodes      = 256
	toolsMaxBranch     = 16
	toolsMaxEnum       = 256
	toolsMaxPatternLen = 512

	toolChoiceMaxNameLen = 64

	responseFormatMaxDepth      = 16
	responseFormatMaxSizeBytes  = 16 * 1024
	responseFormatMaxNodes      = 128
	responseFormatMaxBranch     = 16
	responseFormatMaxEnum       = 256
	responseFormatMaxNameLen    = 64
	responseFormatMaxPatternLen = 512

	structuredOutputsMaxDepth            = 16
	structuredOutputsMaxSizeBytes        = 16 * 1024
	structuredOutputsMaxNodes            = 128
	structuredOutputsMaxBranch           = 16
	structuredOutputsMaxEnum             = 256
	structuredOutputsMaxPatternLen       = 512
	structuredOutputsMaxChoiceEntries    = 256
	structuredOutputsMaxChoiceEntryLen   = 1024
	structuredOutputsMaxGrammarLen       = 8 * 1024
	structuredOutputsMaxGrammarNesting   = 200
	structuredOutputsMaxStructuralTagLen = 4 * 1024
)

// See README.md, "Schema bounds".
var (
	// validSchemaTypes are the JSON-Schema primitives; anything else crashes xgrammar's compiler (CVE-2025-48944).
	validSchemaTypes = map[string]struct{}{
		"string": {}, "number": {}, "integer": {}, "object": {}, "boolean": {}, "array": {}, "null": {},
	}

	forbiddenSchemaKeys = []string{"$ref", "$defs", "definitions"}
	branchSchemaKeys    = []string{"anyOf", "oneOf", "allOf"}

	schemaDataKeys = map[string]struct{}{
		"enum": {}, "const": {}, "default": {}, "examples": {}, "required": {}, "dependentRequired": {},
	}

	schemaChildMapKeys = map[string]struct{}{
		"properties": {}, "patternProperties": {}, "dependentSchemas": {},
	}
)

// SchemaBounds bounds a JSON-Schema payload's shape and size. See README.md, "Schema bounds".
type SchemaBounds struct {
	MaxDepth      int
	MaxNodes      int
	MaxSizeBytes  int
	MaxBranch     int
	MaxEnum       int
	MaxPatternLen int
}

// Check walks schema for structural violations before measuring its serialized size.
func (b SchemaBounds) Check(schema map[string]any) error {
	var nodes int
	if err := b.walk(schema, 1, &nodes); err != nil {
		return err
	}
	return checkSize(schema, b.MaxSizeBytes)
}

func (b SchemaBounds) walk(schema any, depth int, nodes *int) error {
	if depth > b.MaxDepth {
		return depthExceededError(b.MaxDepth)
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	*nodes++
	if *nodes > b.MaxNodes {
		return nodesExceededError(b.MaxNodes)
	}
	for _, forbidden := range forbiddenSchemaKeys {
		if _, exists := object[forbidden]; exists {
			return fmt.Errorf("schema reference keyword is forbidden: %q is not allowed", forbidden)
		}
	}
	if enum, ok := object["enum"].([]any); ok && len(enum) > b.MaxEnum {
		return fmt.Errorf("enum size exceeded: limit %d", b.MaxEnum)
	}
	if err := validateSchemaType(object); err != nil {
		return err
	}
	if err := b.validateSchemaPattern(object); err != nil {
		return err
	}
	for _, branchKey := range branchSchemaKeys {
		if arms, ok := object[branchKey].([]any); ok && len(arms) > b.MaxBranch {
			return fmt.Errorf("schema branch arms exceeded: %s limit %d", branchKey, b.MaxBranch)
		}
	}
	for key, value := range object {
		if _, isData := schemaDataKeys[key]; isData {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			if _, isChildMap := schemaChildMapKeys[key]; isChildMap {
				for _, child := range typed {
					if err := b.walk(child, depth+1, nodes); err != nil {
						return err
					}
				}
			} else if err := b.walk(typed, depth+1, nodes); err != nil {
				return err
			}
		case []any:
			for _, child := range typed {
				if err := b.walk(child, depth+1, nodes); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateSchemaType rejects a `type` that is not a JSON-Schema primitive; absent is fine (CVE-2025-48944).
func validateSchemaType(object map[string]any) error {
	raw, present := object["type"]
	if !present {
		return nil
	}
	switch typed := raw.(type) {
	case string:
		if _, ok := validSchemaTypes[typed]; !ok {
			return fmt.Errorf("schema type is not a valid JSON-Schema primitive: %q", typed)
		}
	case []any:
		for _, entry := range typed {
			name, ok := entry.(string)
			if !ok {
				return fmt.Errorf("schema type is not a valid JSON-Schema primitive: array elements must be strings")
			}
			if _, ok := validSchemaTypes[name]; !ok {
				return fmt.Errorf("schema type is not a valid JSON-Schema primitive: %q", name)
			}
		}
	default:
		return fmt.Errorf("schema type is not a valid JSON-Schema primitive: must be a string or array of strings")
	}
	return nil
}

// validateSchemaPattern rejects an over-long or uncompilable `pattern` (CVE-2025-48944); MaxPatternLen <= 0 disables it.
func (b SchemaBounds) validateSchemaPattern(object map[string]any) error {
	if b.MaxPatternLen <= 0 {
		return nil
	}
	raw, present := object["pattern"]
	if !present {
		return nil
	}
	pattern, ok := raw.(string)
	if !ok {
		return fmt.Errorf("schema pattern is not a valid regular expression: must be a string")
	}
	if len(pattern) > b.MaxPatternLen {
		return fmt.Errorf("schema pattern is not a valid regular expression: length %d exceeds limit %d", len(pattern), b.MaxPatternLen)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("schema pattern is not a valid regular expression: %w", err)
	}
	return nil
}

// ObjectBounds bounds an arbitrary nested JSON object, with no JSON-Schema semantics.
type ObjectBounds struct {
	MaxDepth     int
	MaxNodes     int
	MaxSizeBytes int
}

func (b ObjectBounds) Check(obj map[string]any) error {
	var nodes int
	if err := b.walk(obj, 1, &nodes); err != nil {
		return err
	}
	return checkSize(obj, b.MaxSizeBytes)
}

func (b ObjectBounds) walk(value any, depth int, nodes *int) error {
	if depth > b.MaxDepth {
		return depthExceededError(b.MaxDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		*nodes++
		if *nodes > b.MaxNodes {
			return nodesExceededError(b.MaxNodes)
		}
		for _, child := range typed {
			if err := b.walk(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := b.walk(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

// depthExceededError and nodesExceededError: shared so both walkers render identical rejections.
func depthExceededError(maxDepth int) error {
	return fmt.Errorf("nesting depth exceeded: limit %d", maxDepth)
}

func nodesExceededError(maxNodes int) error {
	return fmt.Errorf("node count exceeded: limit %d", maxNodes)
}

// checkSize rejects when v's marshaled size exceeds maxSizeBytes; maxSizeBytes <= 0 disables it.
func checkSize(v any, maxSizeBytes int) error {
	if maxSizeBytes <= 0 {
		return nil
	}
	size, err := jsonMarshaledSize(v)
	if err != nil {
		return fmt.Errorf("cannot be serialized: %w", err)
	}
	if size > maxSizeBytes {
		return fmt.Errorf("serialized size exceeded: limit %d bytes", maxSizeBytes)
	}
	return nil
}

type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// jsonMarshaledSize returns len(json.Marshal(v)) without allocating it; Encode trails a newline, so subtract one.
func jsonMarshaledSize(v any) (int, error) {
	var counter countingWriter
	if err := json.NewEncoder(&counter).Encode(v); err != nil {
		return 0, err
	}
	return counter.n - 1, nil
}
