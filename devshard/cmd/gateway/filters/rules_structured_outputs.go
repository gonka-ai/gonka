package filters

import (
	"regexp"
	"strings"
)

// structuredOutputsBounds bundles the shared JSON-schema bounds plus the choice/grammar/structural_tag caps.
type structuredOutputsBounds struct {
	SchemaBounds
	MaxChoiceEntries    int
	MaxChoiceEntryLen   int
	MaxGrammarLen       int
	MaxGrammarNesting   int
	MaxStructuralTagLen int
}

func buildStructuredOutputsKnownFields() map[string]struct{} {
	known := make(map[string]struct{}, len(structuredOutputsConstraintFields)+len(structuredOutputsAuxiliaryFields))
	for _, field := range structuredOutputsConstraintFields {
		known[field] = struct{}{}
	}
	for _, field := range structuredOutputsAuxiliaryFields {
		known[field] = struct{}{}
	}
	return known
}

// structuredOutputsConstraintValidators pairs each constraint field with its validator so a new one can't be added without.
func structuredOutputsConstraintValidators(bounds structuredOutputsBounds) map[string]func(value any) error {
	return map[string]func(value any) error{
		"json": func(value any) error {
			schema, ok := value.(map[string]any)
			if !ok {
				return Reject("structured_outputs.json: must be an object (string-encoded schemas are not accepted)")
			}
			if err := bounds.Check(schema); err != nil {
				return Reject("structured_outputs.json: %v", err)
			}
			return nil
		},
		"regex": func(value any) error {
			return validStructuredOutputsPattern(value, bounds.MaxPatternLen, "regex")
		},
		"choice": func(value any) error {
			return validStructuredOutputsChoice(value, bounds.MaxChoiceEntries, bounds.MaxChoiceEntryLen, bounds.MaxSizeBytes)
		},
		"grammar": func(value any) error {
			return validStructuredOutputsGrammar(value, bounds.MaxGrammarLen, bounds.MaxGrammarNesting)
		},
		"json_object": func(value any) error {
			if _, ok := value.(bool); !ok {
				return Reject("structured_outputs.json_object: must be a boolean")
			}
			return nil
		},
		"structural_tag": func(value any) error {
			return validStructuredOutputsStructuralTag(value, bounds.MaxStructuralTagLen)
		},
	}
}

// validStructuredOutputs enforces the closed sub-field whitelist and exactly one constraint. See README.md, "`structured_outputs`".
func validStructuredOutputs(bounds structuredOutputsBounds) RuleFunc {
	constraintValidators := structuredOutputsConstraintValidators(bounds)
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get("structured_outputs")
		if !exists {
			return nil
		}
		if ctx.Profile.rejectsStructuredOutputs() {
			return Reject("structured_outputs: not supported on this model — use response_format instead")
		}
		object, ok := raw.(map[string]any)
		if !ok {
			return Reject("structured_outputs: invalid wrapper shape: must be an object")
		}
		if ctx.Document.Has("response_format") {
			return Reject("structured_outputs: cannot be combined with response_format")
		}
		for _, key := range structuredOutputsPrivateFields {
			delete(object, key)
		}
		for key := range object {
			if _, known := structuredOutputsKnownFields[key]; !known {
				return Reject("structured_outputs: invalid wrapper shape: unknown sub-field %q", key)
			}
		}
		set := 0
		setNames := make([]string, 0, len(structuredOutputsConstraintFields))
		for _, field := range structuredOutputsConstraintFields {
			if value, ok := object[field]; presentField(value, ok) {
				set++
				setNames = append(setNames, field)
			}
		}
		if set != 1 {
			if set == 0 {
				return Reject("structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set (got 0)")
			}
			return Reject("structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set (got %d: %s)", set, strings.Join(setNames, ", "))
		}
		for _, field := range structuredOutputsConstraintFields {
			value, ok := object[field]
			if !presentField(value, ok) {
				continue
			}
			if err := constraintValidators[field](value); err != nil {
				return err
			}
		}
		if value, ok := object["whitespace_pattern"]; presentField(value, ok) {
			if err := validStructuredOutputsPattern(value, bounds.MaxPatternLen, "whitespace_pattern"); err != nil {
				return err
			}
		}
		for _, flag := range []string{"disable_any_whitespace", "disable_additional_properties"} {
			if value, ok := object[flag]; presentField(value, ok) {
				if _, ok := value.(bool); !ok {
					return Reject("structured_outputs: flag must be a boolean: %s", flag)
				}
			}
		}
		return nil
	}
}

// validStructuredOutputsPattern backs both "regex" and "whitespace_pattern"; only the message prefix differs.
func validStructuredOutputsPattern(value any, maxLen int, fieldName string) error {
	pattern, ok := value.(string)
	if !ok {
		return Reject("structured_outputs.%s: invalid shape: must be a string", fieldName)
	}
	if len(pattern) > maxLen {
		return Reject("structured_outputs.%s: length exceeded: %d > %d", fieldName, len(pattern), maxLen)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return Reject("structured_outputs.%s: must compile as a regex: %v", fieldName, err)
	}
	return nil
}

func validStructuredOutputsChoice(value any, maxEntries, maxEntryLen, maxTotalBytes int) error {
	choices, ok := value.([]any)
	if !ok || len(choices) == 0 {
		return Reject("structured_outputs.choice: must be a non-empty string array")
	}
	if len(choices) > maxEntries {
		return Reject("structured_outputs.choice: exceeded size limits: %d entries > %d", len(choices), maxEntries)
	}
	total := 0
	for index, item := range choices {
		entry, ok := item.(string)
		if !ok {
			return Reject("structured_outputs.choice: must be a non-empty string array: choice[%d] must be a string", index)
		}
		if len(entry) > maxEntryLen {
			return Reject("structured_outputs.choice: exceeded size limits: choice[%d] length %d > %d", index, len(entry), maxEntryLen)
		}
		total += len(entry)
		if total > maxTotalBytes {
			return Reject("structured_outputs.choice: exceeded size limits: total length %d > %d", total, maxTotalBytes)
		}
	}
	return nil
}

// validStructuredOutputsGrammar tracks active bracket depth: unmatched opens (the CVE-2026-25048 shape) never come back down.
func validStructuredOutputsGrammar(value any, maxLen, maxNesting int) error {
	grammar, ok := value.(string)
	if !ok {
		return Reject("structured_outputs.grammar: invalid shape: must be a string")
	}
	if len(grammar) > maxLen {
		return Reject("structured_outputs.grammar: length exceeded: %d > %d", len(grammar), maxLen)
	}
	depth, maxDepth := 0, 0
	for _, r := range grammar {
		switch r {
		case '(', '[', '{':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
		if maxDepth > maxNesting {
			return Reject("structured_outputs.grammar: nesting depth exceeded: %d > %d", maxDepth, maxNesting)
		}
	}
	return nil
}

// validStructuredOutputsStructuralTag accepts only the object form; a JSON-encoded string crashes the engine.
func validStructuredOutputsStructuralTag(value any, maxLen int) error {
	object, ok := value.(map[string]any)
	if !ok {
		return Reject("structured_outputs.structural_tag: invalid shape: must be an object (a JSON-encoded string crashes the engine)")
	}
	size, err := jsonMarshaledSize(object)
	if err != nil {
		return Reject("structured_outputs.structural_tag: invalid shape: %v", err)
	}
	if size > maxLen {
		return Reject("structured_outputs.structural_tag: length exceeded: %d > %d", size, maxLen)
	}
	return nil
}
