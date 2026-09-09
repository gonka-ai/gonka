package paramvalidators

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"devshard/cmd/devshardctl/filtercore"
)

var (
	ErrStructuredOutputsShape                    = errors.New("structured_outputs: invalid wrapper shape")
	ErrStructuredOutputsExactlyOne               = errors.New("structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set")
	ErrStructuredOutputsResponseFormatConflict   = errors.New("structured_outputs: cannot be combined with response_format")
	ErrStructuredOutputsNotSupportedOnRoute      = errors.New("structured_outputs: not supported on this model — use response_format instead")
	ErrStructuredOutputsJSONShape                = errors.New("structured_outputs.json: must be an object (string-encoded schemas are not accepted)")
	ErrStructuredOutputsRegexShape               = errors.New("structured_outputs.regex: invalid shape")
	ErrStructuredOutputsRegexLength              = errors.New("structured_outputs.regex: length exceeded")
	ErrStructuredOutputsRegexCompile             = errors.New("structured_outputs.regex: must compile as a regex")
	ErrStructuredOutputsChoiceShape              = errors.New("structured_outputs.choice: must be a non-empty string array")
	ErrStructuredOutputsChoiceLimit              = errors.New("structured_outputs.choice: exceeded size limits")
	ErrStructuredOutputsGrammarShape             = errors.New("structured_outputs.grammar: invalid shape")
	ErrStructuredOutputsGrammarLength            = errors.New("structured_outputs.grammar: length exceeded")
	ErrStructuredOutputsGrammarNesting           = errors.New("structured_outputs.grammar: nesting depth exceeded")
	ErrStructuredOutputsJSONObjectShape          = errors.New("structured_outputs.json_object: must be a boolean")
	ErrStructuredOutputsStructuralTagShape       = errors.New("structured_outputs.structural_tag: invalid shape")
	ErrStructuredOutputsStructuralTagLength      = errors.New("structured_outputs.structural_tag: length exceeded")
	ErrStructuredOutputsWhitespacePatternShape   = errors.New("structured_outputs.whitespace_pattern: invalid shape")
	ErrStructuredOutputsWhitespacePatternLength  = errors.New("structured_outputs.whitespace_pattern: length exceeded")
	ErrStructuredOutputsWhitespacePatternCompile = errors.New("structured_outputs.whitespace_pattern: must compile as a regex")
	ErrStructuredOutputsBoolFlagShape            = errors.New("structured_outputs: flag must be a boolean")
)

// structuredOutputsConstraintFields are the mutually-exclusive constraint sub-fields per
// vLLM's StructuredOutputsParams.__post_init__ rule (count of set fields must equal 1).
// Source: https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py
var structuredOutputsConstraintFields = []string{
	"json", "regex", "choice", "grammar", "json_object", "structural_tag",
}

// structuredOutputsAuxiliaryFields are documented modifiers that may co-exist with any
// constraint field per vLLM's StructuredOutputsParams.
var structuredOutputsAuxiliaryFields = []string{
	"whitespace_pattern", "disable_any_whitespace", "disable_additional_properties",
}

// structuredOutputsPrivateFields are documented as `field(default=None, init=False)` in vLLM
// source — internal back-end state, never client-settable. Stripped silently.
var structuredOutputsPrivateFields = []string{"_backend", "_backend_was_auto"}

// structuredOutputsKnownFields is the closed allow-list of sub-keys. Anything outside this
// set is rejected with 400 — preserves the gateway's whitelist contract inside the envelope.
var structuredOutputsKnownFields = func() map[string]struct{} {
	m := make(map[string]struct{}, len(structuredOutputsConstraintFields)+len(structuredOutputsAuxiliaryFields))
	for _, f := range structuredOutputsConstraintFields {
		m[f] = struct{}{}
	}
	for _, f := range structuredOutputsAuxiliaryFields {
		m[f] = struct{}{}
	}
	return m
}()

type StructuredOutputsValidator struct {
	// RejectedModels rejects the entire field with 400 when ctx.RoutedModel matches.
	// Used to gate Moonshot Kimi K2.6 (Moonshot does not declare structured_outputs).
	RejectedModels []string

	// json schema bounds — match ResponseFormatValidator (same xgrammar codepath).
	MaxDepth      int
	MaxSize       int
	MaxNodes      int
	MaxBranch     int
	MaxEnum       int
	MaxPatternLen int

	// choice / grammar / structural_tag specific caps.
	MaxChoiceEntries    int
	MaxChoiceEntryLen   int
	MaxGrammarLen       int
	MaxGrammarNesting   int
	MaxStructuralTagLen int
}

func (v StructuredOutputsValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["structured_outputs"]
	if !exists {
		return nil
	}
	if filtercore.MatchesModel(vctx.RoutedModel, v.RejectedModels) {
		return fmt.Errorf("%w", ErrStructuredOutputsNotSupportedOnRoute)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrStructuredOutputsShape)
	}
	// vLLM merges `response_format` into `structured_outputs` via `dataclasses.replace()`
	// in chat_completion/protocol.py — the merged dataclass then trips
	// `StructuredOutputsParams.__post_init__`'s exactly-one rule and surfaces as a 400 with
	// a leaky pydantic dump that exposes private internal fields (`_backend`, etc.).
	// Gateway 400 pre-empts the broker round-trip and returns a clean targeted error.
	if _, conflicts := vctx.Document["response_format"]; conflicts {
		return fmt.Errorf("%w", ErrStructuredOutputsResponseFormatConflict)
	}
	// Strip private fields only after early rejections — mutation is a commitment that
	// validation will run to completion.
	for _, k := range structuredOutputsPrivateFields {
		delete(obj, k)
	}
	for key := range obj {
		if _, known := structuredOutputsKnownFields[key]; !known {
			return fmt.Errorf("%w: unknown sub-field %q", ErrStructuredOutputsShape, key)
		}
	}
	// vLLM's __post_init__ counts `is not None`, so a wire-explicit null is treated as absent.
	set := 0
	setNames := make([]string, 0, len(structuredOutputsConstraintFields))
	for _, f := range structuredOutputsConstraintFields {
		if value, ok := obj[f]; ok && value != nil {
			set++
			setNames = append(setNames, f)
		}
	}
	if set != 1 {
		if set == 0 {
			return fmt.Errorf("%w (got 0)", ErrStructuredOutputsExactlyOne)
		}
		return fmt.Errorf("%w (got %d: %s)", ErrStructuredOutputsExactlyOne, set, strings.Join(setNames, ", "))
	}
	if value, ok := obj["json"]; ok && value != nil {
		if err := v.validateJSON(value); err != nil {
			return err
		}
	}
	if value, ok := obj["regex"]; ok && value != nil {
		if err := v.validateRegexString(value,
			ErrStructuredOutputsRegexShape,
			ErrStructuredOutputsRegexLength,
			ErrStructuredOutputsRegexCompile); err != nil {
			return err
		}
	}
	if value, ok := obj["choice"]; ok && value != nil {
		if err := v.validateChoice(value); err != nil {
			return err
		}
	}
	if value, ok := obj["grammar"]; ok && value != nil {
		if err := v.validateGrammar(value); err != nil {
			return err
		}
	}
	if value, ok := obj["json_object"]; ok && value != nil {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%w", ErrStructuredOutputsJSONObjectShape)
		}
	}
	if value, ok := obj["structural_tag"]; ok && value != nil {
		if err := v.validateStructuralTag(value); err != nil {
			return err
		}
	}
	if value, ok := obj["whitespace_pattern"]; ok && value != nil {
		if err := v.validateRegexString(value,
			ErrStructuredOutputsWhitespacePatternShape,
			ErrStructuredOutputsWhitespacePatternLength,
			ErrStructuredOutputsWhitespacePatternCompile); err != nil {
			return err
		}
	}
	for _, flag := range []string{"disable_any_whitespace", "disable_additional_properties"} {
		if value, ok := obj[flag]; ok && value != nil {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%w: %s", ErrStructuredOutputsBoolFlagShape, flag)
			}
		}
	}
	return nil
}

func (v StructuredOutputsValidator) validateJSON(value any) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w", ErrStructuredOutputsJSONShape)
	}
	bounds := SchemaBounds{
		MaxDepth:      v.MaxDepth,
		MaxSize:       v.MaxSize,
		MaxNodes:      v.MaxNodes,
		MaxBranch:     v.MaxBranch,
		MaxEnum:       v.MaxEnum,
		MaxPatternLen: v.MaxPatternLen,
	}
	if err := bounds.Walk(schema); err != nil {
		return fmt.Errorf("structured_outputs.json: %w", err)
	}
	if err := bounds.CheckSize(schema); err != nil {
		return fmt.Errorf("structured_outputs.json: %w", err)
	}
	return nil
}

// validateRegexString probes the pattern with Go's `regexp.Compile`. This catches
// malformed patterns and bounds the length, but does NOT detect catastrophic-backtracking
// shapes that only Python `re` is vulnerable to. Defense-in-depth only — full
// linear-time guarantee relies on xgrammar's Rust regex on the vLLM side.
func (v StructuredOutputsValidator) validateRegexString(value any, shapeErr, lenErr, compileErr error) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("%w: must be a string", shapeErr)
	}
	if len(s) > v.MaxPatternLen {
		return fmt.Errorf("%w: %d > %d", lenErr, len(s), v.MaxPatternLen)
	}
	if _, err := regexp.Compile(s); err != nil {
		return fmt.Errorf("%w: %v", compileErr, err)
	}
	return nil
}

func (v StructuredOutputsValidator) validateChoice(value any) error {
	arr, ok := value.([]any)
	if !ok || len(arr) == 0 {
		return fmt.Errorf("%w", ErrStructuredOutputsChoiceShape)
	}
	if len(arr) > v.MaxChoiceEntries {
		return fmt.Errorf("%w: %d entries > %d", ErrStructuredOutputsChoiceLimit, len(arr), v.MaxChoiceEntries)
	}
	total := 0
	for i, item := range arr {
		s, ok := item.(string)
		if !ok {
			return fmt.Errorf("%w: choice[%d] must be a string", ErrStructuredOutputsChoiceShape, i)
		}
		if len(s) > v.MaxChoiceEntryLen {
			return fmt.Errorf("%w: choice[%d] length %d > %d", ErrStructuredOutputsChoiceLimit, i, len(s), v.MaxChoiceEntryLen)
		}
		total += len(s)
		if total > v.MaxSize {
			return fmt.Errorf("%w: total length %d > %d", ErrStructuredOutputsChoiceLimit, total, v.MaxSize)
		}
	}
	return nil
}

// validateGrammar tracks active bracket depth ('(', '[', '{' open; matching close decrements).
// CVE-2026-25048 PoC is `'(' × 30000` — unmatched opens. Matching closes do not increase risk.
// Bracket literals inside quoted strings are also counted (false-positive defense-in-depth).
//
// MaxGrammarNesting is set defensively at 200 — well below xgrammar's internal 1000 cap
// (post-CVE-2026-25048 fix in 0.1.32+). Revisit if production traffic legitimately requires
// deeper EBNF; raising it does not re-introduce the CVE class as long as xgrammar is ≥ 0.1.32.
func (v StructuredOutputsValidator) validateGrammar(value any) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("%w: must be a string", ErrStructuredOutputsGrammarShape)
	}
	if len(s) > v.MaxGrammarLen {
		return fmt.Errorf("%w: %d > %d", ErrStructuredOutputsGrammarLength, len(s), v.MaxGrammarLen)
	}
	depth, maxDepth := 0, 0
	for _, ch := range s {
		switch ch {
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
		if maxDepth > v.MaxGrammarNesting {
			return fmt.Errorf("%w: %d > %d", ErrStructuredOutputsGrammarNesting, maxDepth, v.MaxGrammarNesting)
		}
	}
	return nil
}

// validateStructuralTag accepts only the OBJECT form (vLLM's
// StructuralTagResponseFormat: {type, structures[], triggers[]}). A JSON-encoded
// STRING form crashes the engine with an HTTP 500 on the request handler, so it
// is rejected here regardless of route. Size is bounded on the serialized object.
func (v StructuredOutputsValidator) validateStructuralTag(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object (a JSON-encoded string crashes the engine)", ErrStructuredOutputsStructuralTagShape)
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStructuredOutputsStructuralTagShape, err)
	}
	if len(encoded) > v.MaxStructuralTagLen {
		return fmt.Errorf("%w: %d > %d", ErrStructuredOutputsStructuralTagLength, len(encoded), v.MaxStructuralTagLen)
	}
	return nil
}
