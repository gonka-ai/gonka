package paramvalidators

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Schema-bounds sentinels. Returned by SchemaBounds.Walk / SchemaBounds.CheckSize and by
// ObjectBounds.Walk. Callers wrap them with a field-path prefix (e.g.
// "response_format.json_schema.schema", "tools[3].function.parameters",
// "chat_template_kwargs") so the user-facing message points at the offending field while
// errors.Is keeps working for the rejection category.
var (
	ErrSchemaDepth   = errors.New("nesting depth exceeded")
	ErrSchemaNodes   = errors.New("node count exceeded")
	ErrSchemaSize    = errors.New("serialized size exceeded")
	ErrSchemaRef     = errors.New("schema reference keyword is forbidden")
	ErrSchemaEnum    = errors.New("enum size exceeded")
	ErrSchemaBranch  = errors.New("schema branch arms exceeded")
	ErrSchemaType    = errors.New("schema type is not a valid JSON-Schema primitive")
	ErrSchemaPattern = errors.New("schema pattern is not a valid regular expression")
)

// validSchemaTypes lists the JSON Schema primitive type names. Anything else (e.g.
// "something") crashes xgrammar's C++ grammar compiler -- see CVE-2025-48944.
var validSchemaTypes = map[string]struct{}{
	"string":  {},
	"number":  {},
	"integer": {},
	"object":  {},
	"boolean": {},
	"array":   {},
	"null":    {},
}

// SchemaBounds enforces the structural bounds that keep a JSON-Schema payload from
// exploding vLLM's grammar compiler. It is the JSON-Schema-aware walker reused by both
// `response_format.json_schema.schema` and `tools[].function.parameters` (they hit the same
// upstream compiler path).
type SchemaBounds struct {
	MaxDepth  int
	MaxSize   int
	MaxNodes  int
	MaxBranch int // anyOf / oneOf / allOf array arms
	MaxEnum   int // enum entries
	// MaxPatternLen caps the byte length of any `pattern` regex string before we try to
	// compile it. 0 disables both the length check AND the compile-check. Set to a small
	// positive value (e.g. 512) to defuse CVE-2025-48944-class crashes where an unclosed
	// regex (`{`, `(`) kills vLLM's regex engine.
	MaxPatternLen int
}

// Walk recursively traverses the schema, enforcing depth/nodes/branch/enum bounds and the
// $ref/$defs/definitions ban. Walking is done before json.Marshal so attacker-controlled
// deeply nested payloads bail out at the depth check (~O(MaxNodes)) instead of after a full
// O(input size) marshal pass.
func (b SchemaBounds) Walk(schema map[string]any) error {
	var nodes int
	return b.walk(schema, 1, &nodes)
}

// CheckSize rejects when the serialized size exceeds MaxSize. Run AFTER Walk so
// depth/breadth attacks rejected by the walker never pay for serialization.
// MaxSize=0 disables the check.
func (b SchemaBounds) CheckSize(schema map[string]any) error {
	if b.MaxSize <= 0 {
		return nil
	}
	size, err := jsonMarshaledSize(schema)
	if err != nil {
		return fmt.Errorf("cannot be serialized: %v", err)
	}
	if size > b.MaxSize {
		return fmt.Errorf("%w: limit %d bytes", ErrSchemaSize, b.MaxSize)
	}
	return nil
}

func (b SchemaBounds) walk(schema any, depth int, nodes *int) error {
	if depth > b.MaxDepth {
		return fmt.Errorf("%w: limit %d", ErrSchemaDepth, b.MaxDepth)
	}
	obj, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	*nodes++
	if *nodes > b.MaxNodes {
		return fmt.Errorf("%w: limit %d", ErrSchemaNodes, b.MaxNodes)
	}
	for _, forbidden := range forbiddenSchemaKeys {
		if _, exists := obj[forbidden]; exists {
			return fmt.Errorf("%w: %q is not allowed", ErrSchemaRef, forbidden)
		}
	}
	if enum, ok := obj["enum"].([]any); ok && len(enum) > b.MaxEnum {
		return fmt.Errorf("%w: limit %d", ErrSchemaEnum, b.MaxEnum)
	}
	if err := validateSchemaTypeField(obj); err != nil {
		return err
	}
	if err := b.validateSchemaPatternField(obj); err != nil {
		return err
	}
	for _, branchKey := range branchSchemaKeys {
		if arr, ok := obj[branchKey].([]any); ok && len(arr) > b.MaxBranch {
			return fmt.Errorf("%w: %s limit %d", ErrSchemaBranch, branchKey, b.MaxBranch)
		}
	}
	for key, value := range obj {
		if _, isData := responseFormatDataKeys[key]; isData {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			if _, isChildMap := responseFormatChildMapKeys[key]; isChildMap {
				for _, child := range typed {
					if err := b.walk(child, depth+1, nodes); err != nil {
						return err
					}
				}
			} else {
				if err := b.walk(typed, depth+1, nodes); err != nil {
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
	}
	return nil
}

// ObjectBounds enforces depth/nodes/size on an arbitrary JSON object that is NOT a JSON
// Schema -- it has no special data-carrier keys, no $ref ban, no anyOf/enum semantics. Used
// for fields like `chat_template_kwargs` that feed vLLM's Jinja template renderer: we only
// care about bounding total structure, not validating individual JSON Schema constructs.
type ObjectBounds struct {
	MaxDepth int
	MaxSize  int
	MaxNodes int
}

// Walk recursively traverses every object/array node. Returns ErrSchemaDepth/ErrSchemaNodes
// (sentinels are shared with SchemaBounds because the rejection class is the same).
func (b ObjectBounds) Walk(obj map[string]any) error {
	var nodes int
	return b.walk(obj, 1, &nodes)
}

// CheckSize behaves identically to SchemaBounds.CheckSize.
func (b ObjectBounds) CheckSize(obj map[string]any) error {
	if b.MaxSize <= 0 {
		return nil
	}
	size, err := jsonMarshaledSize(obj)
	if err != nil {
		return fmt.Errorf("cannot be serialized: %v", err)
	}
	if size > b.MaxSize {
		return fmt.Errorf("%w: limit %d bytes", ErrSchemaSize, b.MaxSize)
	}
	return nil
}

type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// jsonMarshaledSize returns len(json.Marshal(v)) without allocating the output
// slice. Encoder.Encode trails a newline that Marshal omits, so subtract one.
func jsonMarshaledSize(v any) (int, error) {
	var cw countingWriter
	if err := json.NewEncoder(&cw).Encode(v); err != nil {
		return 0, err
	}
	return cw.n - 1, nil
}

func (b ObjectBounds) walk(value any, depth int, nodes *int) error {
	if depth > b.MaxDepth {
		return fmt.Errorf("%w: limit %d", ErrSchemaDepth, b.MaxDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		*nodes++
		if *nodes > b.MaxNodes {
			return fmt.Errorf("%w: limit %d", ErrSchemaNodes, b.MaxNodes)
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

// validateSchemaTypeField rejects schema nodes whose `type` is not a JSON-Schema primitive
// (`string`, `number`, `integer`, `object`, `boolean`, `array`, `null`) or an array of
// primitives. Anything else (e.g. `type: "something"`) crashes xgrammar's C++ grammar
// compiler -- see CVE-2025-48944. Schemas that omit `type` entirely are fine; this only
// rejects an explicitly bad value.
func validateSchemaTypeField(obj map[string]any) error {
	raw, present := obj["type"]
	if !present {
		return nil
	}
	switch typed := raw.(type) {
	case string:
		if _, ok := validSchemaTypes[typed]; !ok {
			return fmt.Errorf("%w: %q", ErrSchemaType, typed)
		}
	case []any:
		for _, v := range typed {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%w: array elements must be strings", ErrSchemaType)
			}
			if _, ok := validSchemaTypes[s]; !ok {
				return fmt.Errorf("%w: %q", ErrSchemaType, s)
			}
		}
	default:
		return fmt.Errorf("%w: must be a string or array of strings", ErrSchemaType)
	}
	return nil
}

// validateSchemaPatternField rejects schema nodes whose `pattern` exceeds MaxPatternLen
// or fails to compile as a Go regular expression. The compile step catches the
// CVE-2025-48944 case where `pattern: "{"` (unclosed group) crashes vLLM's regex engine
// at grammar-compile time. Go's RE2 is more permissive than xgrammar's engine, so a Go
// compile success does not *guarantee* xgrammar success -- but a Go failure DOES guarantee
// xgrammar failure for the same syntactic shape, which is what defuses the documented CVE.
// Skipped entirely when MaxPatternLen == 0.
func (b SchemaBounds) validateSchemaPatternField(obj map[string]any) error {
	if b.MaxPatternLen <= 0 {
		return nil
	}
	raw, present := obj["pattern"]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%w: must be a string", ErrSchemaPattern)
	}
	if len(s) > b.MaxPatternLen {
		return fmt.Errorf("%w: length %d exceeds limit %d", ErrSchemaPattern, len(s), b.MaxPatternLen)
	}
	if _, err := regexp.Compile(s); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaPattern, err)
	}
	return nil
}
