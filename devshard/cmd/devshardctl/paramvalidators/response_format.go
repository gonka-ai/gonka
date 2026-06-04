// Package paramvalidators hosts pure validators for individual chat-completion request fields.
// Each validator operates on the decoded request document (map[string]any) without any
// dependency on the main package's pipeline types, so it can be unit-tested in isolation and
// composed back into the catalog through a thin adapter.
package paramvalidators

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Sentinel errors specific to response_format wrapper shape. Schema-walk rejection
// categories (depth/nodes/size/ref/enum/branch/type/pattern) live in schema_bounds.go
// as ErrSchema* and are returned wrapped through here.
var (
	ErrResponseFormatShape       = errors.New("response_format: invalid wrapper shape")
	ErrResponseFormatType        = errors.New("response_format.type: missing or unsupported")
	ErrResponseFormatJSONSchema  = errors.New("response_format.json_schema: invalid wrapper shape")
	ErrResponseFormatName        = errors.New("response_format.json_schema.name: invalid")
	ErrResponseFormatSchemaShape = errors.New("response_format.json_schema.schema: invalid shape")
)

// ResponseFormatValidator bounds an OpenAI-compatible response_format payload before it is
// forwarded to vLLM. A pathological json_schema (deep recursion, huge byte size, runaway
// breadth, schema $refs) can crash the upstream grammar compiler, so any violation must
// reject the request before it leaves the gateway.
type ResponseFormatValidator struct {
	MaxDepth      int
	MaxSize       int
	MaxNodes      int
	MaxBranch     int
	MaxEnum       int
	MaxNameLen    int
	MaxPatternLen int
}

var responseFormatNameRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// forbiddenSchemaKeys and branchSchemaKeys are walked once per node. Defining them at package
// scope keeps the slice headers off the per-call allocation path (the literal-in-range form
// allocates a fresh backing array on every walkSchema invocation).
var forbiddenSchemaKeys = []string{"$ref", "$defs", "definitions"}
var branchSchemaKeys = []string{"anyOf", "oneOf", "allOf"}

// responseFormatDataKeys lists JSON-Schema keywords whose values are *literal data*, not
// child schemas. They must NOT be recursed into; an attacker could otherwise put a deeply
// nested object inside `default`/`examples`/`const` and have it counted against the schema
// budget needlessly, or worse, hide structure the walker treats as schema-shaped.
var responseFormatDataKeys = map[string]struct{}{
	"enum":              {},
	"const":             {},
	"default":           {},
	"examples":          {},
	"required":          {},
	"dependentRequired": {},
}

// responseFormatChildMapKeys lists keywords whose values are *maps* of name->schema (not a
// schema themselves). We recurse into each map value as a separate child schema; the wrapper
// map itself is not counted as a schema node.
var responseFormatChildMapKeys = map[string]struct{}{
	"properties":        {},
	"patternProperties": {},
	"dependentSchemas":  {},
}

// Validate inspects the "response_format" entry of the given document. Returns nil if
// response_format is absent, has type text/json_object, or has a json_schema payload that
// fits within all configured bounds.
func (v ResponseFormatValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["response_format"]
	if !exists {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrResponseFormatShape)
	}
	typeValue, ok := obj["type"].(string)
	if !ok || strings.TrimSpace(typeValue) == "" {
		return fmt.Errorf("%w: must be a non-empty string", ErrResponseFormatType)
	}
	switch typeValue {
	case "text", "json_object":
		return nil
	case "json_schema":
		return v.validateJSONSchemaWrapper(obj)
	default:
		return fmt.Errorf("%w: %q is not supported (allowed: text, json_object, json_schema)", ErrResponseFormatType, typeValue)
	}
}

func (v ResponseFormatValidator) validateJSONSchemaWrapper(rf map[string]any) error {
	wrapper, ok := rf["json_schema"].(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrResponseFormatJSONSchema)
	}
	name, ok := wrapper["name"].(string)
	if !ok || name == "" {
		return fmt.Errorf("%w: must be a non-empty string", ErrResponseFormatName)
	}
	if len(name) > v.MaxNameLen {
		return fmt.Errorf("%w: must be %d characters or fewer", ErrResponseFormatName, v.MaxNameLen)
	}
	if !responseFormatNameRegex.MatchString(name) {
		return fmt.Errorf("%w: must match %s", ErrResponseFormatName, responseFormatNameRegex.String())
	}
	schema, ok := wrapper["schema"].(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrResponseFormatSchemaShape)
	}
	bounds := SchemaBounds{
		MaxDepth:      v.MaxDepth,
		MaxSize:       v.MaxSize,
		MaxNodes:      v.MaxNodes,
		MaxBranch:     v.MaxBranch,
		MaxEnum:       v.MaxEnum,
		MaxPatternLen: v.MaxPatternLen,
	}
	// Walk first so depth/nodes/breadth attacks bail out without ever paying for json.Marshal
	// inside CheckSize. json.Marshal is O(input size) and would otherwise serialize an
	// attacker-controlled depth-200 payload in full before the depth check ever fires.
	if err := bounds.Walk(schema); err != nil {
		return fmt.Errorf("response_format.json_schema.schema: %w", err)
	}
	if err := bounds.CheckSize(schema); err != nil {
		return fmt.Errorf("response_format.json_schema.schema: %w", err)
	}
	return nil
}
