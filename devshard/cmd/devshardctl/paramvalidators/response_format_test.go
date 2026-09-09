package paramvalidators

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultResponseFormatValidator() ResponseFormatValidator {
	return ResponseFormatValidator{
		MaxDepth:      16,
		MaxSize:       16 * 1024,
		MaxNodes:      128,
		MaxBranch:     16,
		MaxEnum:       256,
		MaxNameLen:    64,
		MaxPatternLen: 512,
	}
}

func parseDocument(tb testing.TB, body string) map[string]any {
	tb.Helper()
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		tb.Fatalf("parse: %v", err)
	}
	return raw
}

func TestResponseFormatValidatorAccepts(t *testing.T) {
	v := defaultResponseFormatValidator()
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "type text", body: `{"response_format":{"type":"text"}}`},
		{name: "type json_object", body: `{"response_format":{"type":"json_object"}}`},
		{name: "json_schema simple", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"weather_v1","schema":{"type":"object","properties":{"city":{"type":"string"},"temp":{"type":"number"}},"required":["city"]}}}}`},
		{name: "json_schema at depth limit", body: jsonSchemaResponseFormatBody(nestedPropertiesSchema(16))},
		{name: "json_schema with anyOf at branch limit", body: jsonSchemaResponseFormatBody(`{"anyOf":[` + strings.Repeat(`{"type":"string"},`, 15) + `{"type":"string"}]}`)},
		{name: "json_schema with enum at limit", body: jsonSchemaResponseFormatBody(bigEnumSchema(256))},
		{name: "json_schema name with dots dashes underscores", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"abc_DEF-1.2","schema":{"type":"object"}}}}`},
		// type can be an array of primitives (JSON Schema draft 6+); accept that shape.
		{name: "schema type as array of primitives", body: jsonSchemaResponseFormatBody(`{"type":["string","null"]}`)},
		// A reasonable regex pattern under the length cap compiles and is accepted.
		{name: "schema with valid pattern", body: jsonSchemaResponseFormatBody(`{"type":"string","pattern":"^[a-zA-Z0-9_-]+$"}`)},
		// Co-boundary: hit MaxDepth=16 and MaxNodes=128 simultaneously. 15 chain levels
		// + 113 leaf properties = 128 nodes, leaves sit at depth 16.
		{name: "depth and nodes both at limit", body: jsonSchemaResponseFormatBody(coBoundarySchema(15, 113))},
		// Size boundary: exactly MaxSize=16384 bytes after json.Marshal. > MaxSize rejects.
		{name: "marshalled size exactly at limit", body: jsonSchemaResponseFormatBody(schemaOfMarshalledSize(t, 16*1024))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		})
	}
}

func TestResponseFormatValidatorRejects(t *testing.T) {
	v := defaultResponseFormatValidator()
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "response_format not an object", body: `{"response_format":"hi"}`, wantErr: ErrResponseFormatShape},
		{name: "type missing", body: `{"response_format":{"json_schema":{"name":"r","schema":{"type":"object"}}}}`, wantErr: ErrResponseFormatType},
		{name: "type empty string", body: `{"response_format":{"type":""}}`, wantErr: ErrResponseFormatType},
		{name: "unknown type", body: `{"response_format":{"type":"banana"}}`, wantErr: ErrResponseFormatType},
		{name: "json_schema wrapper missing", body: `{"response_format":{"type":"json_schema"}}`, wantErr: ErrResponseFormatJSONSchema},
		{name: "json_schema missing name", body: `{"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`, wantErr: ErrResponseFormatName},
		{name: "json_schema whitespace name", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"   ","schema":{"type":"object"}}}}`, wantErr: ErrResponseFormatName},
		{name: "json_schema name has bad chars", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"bad name","schema":{"type":"object"}}}}`, wantErr: ErrResponseFormatName},
		{name: "json_schema name too long", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"` + strings.Repeat("a", 65) + `","schema":{"type":"object"}}}}`, wantErr: ErrResponseFormatName},
		{name: "json_schema missing schema", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"r"}}}`, wantErr: ErrResponseFormatSchemaShape},
		{name: "schema not an object", body: `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":"x"}}}`, wantErr: ErrResponseFormatSchemaShape},
		{name: "depth exceeds limit", body: jsonSchemaResponseFormatBody(nestedPropertiesSchema(17)), wantErr: ErrSchemaDepth},
		{name: "deep recursion attack", body: jsonSchemaResponseFormatBody(nestedPropertiesSchema(200)), wantErr: ErrSchemaDepth},
		{name: "schema size exceeds 16 KiB", body: jsonSchemaResponseFormatBody(`{"type":"object","properties":{"` + strings.Repeat("a", 17*1024) + `":{"type":"string"}}}`), wantErr: ErrSchemaSize},
		// Size boundary: MaxSize+1 must reject. CheckSize uses `> MaxSize`, so the off-by-one
		// risk is right here.
		{name: "marshalled size one byte over limit", body: jsonSchemaResponseFormatBody(schemaOfMarshalledSize(t, 16*1024+1)), wantErr: ErrSchemaSize},
		{name: "schema node count exceeds 128", body: jsonSchemaResponseFormatBody(manyPropertiesSchema(200)), wantErr: ErrSchemaNodes},
		// Node boundary: manyPropertiesSchema(128) = 1 root + 128 children = 129 nodes, one over.
		{name: "node count one over limit", body: jsonSchemaResponseFormatBody(manyPropertiesSchema(128)), wantErr: ErrSchemaNodes},
		{name: "ref not allowed", body: jsonSchemaResponseFormatBody(`{"$ref":"#/foo"}`), wantErr: ErrSchemaRef},
		{name: "defs not allowed", body: jsonSchemaResponseFormatBody(`{"$defs":{"x":{}}}`), wantErr: ErrSchemaRef},
		{name: "definitions not allowed", body: jsonSchemaResponseFormatBody(`{"definitions":{"x":{}}}`), wantErr: ErrSchemaRef},
		{name: "anyOf exceeds branch limit", body: jsonSchemaResponseFormatBody(`{"anyOf":[` + strings.Repeat(`{"type":"string"},`, 16) + `{"type":"string"}]}`), wantErr: ErrSchemaBranch},
		{name: "oneOf exceeds branch limit", body: jsonSchemaResponseFormatBody(`{"oneOf":[` + strings.Repeat(`{"type":"string"},`, 16) + `{"type":"string"}]}`), wantErr: ErrSchemaBranch},
		{name: "allOf exceeds branch limit", body: jsonSchemaResponseFormatBody(`{"allOf":[` + strings.Repeat(`{"type":"string"},`, 16) + `{"type":"string"}]}`), wantErr: ErrSchemaBranch},
		{name: "enum exceeds limit", body: jsonSchemaResponseFormatBody(bigEnumSchema(257)), wantErr: ErrSchemaEnum},
		{name: "ref deep inside properties", body: jsonSchemaResponseFormatBody(`{"type":"object","properties":{"x":{"$ref":"#/y"}}}`), wantErr: ErrSchemaRef},
		// Regression: walker must traverse every schema-valued keyword. Hiding a deep nest or a
		// $ref under if/then/else/contains/not/propertyNames/unevaluated*/dependentSchemas
		// previously bypassed the validator.
		{name: "depth via if chain", body: jsonSchemaResponseFormatBody(nestedIfSchema(200)), wantErr: ErrSchemaDepth},
		{name: "depth via then chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("then", 200)), wantErr: ErrSchemaDepth},
		{name: "depth via else chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("else", 200)), wantErr: ErrSchemaDepth},
		{name: "depth via contains chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("contains", 200)), wantErr: ErrSchemaDepth},
		{name: "depth via not chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("not", 200)), wantErr: ErrSchemaDepth},
		{name: "depth via propertyNames chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("propertyNames", 200)), wantErr: ErrSchemaDepth},
		{name: "depth via unevaluatedProperties chain", body: jsonSchemaResponseFormatBody(nestedKeywordSchema("unevaluatedProperties", 200)), wantErr: ErrSchemaDepth},
		{name: "ref hidden under if", body: jsonSchemaResponseFormatBody(`{"if":{"$ref":"#/x"}}`), wantErr: ErrSchemaRef},
		{name: "ref hidden under contains", body: jsonSchemaResponseFormatBody(`{"contains":{"$ref":"#/x"}}`), wantErr: ErrSchemaRef},
		{name: "ref hidden under not", body: jsonSchemaResponseFormatBody(`{"not":{"$ref":"#/x"}}`), wantErr: ErrSchemaRef},
		{name: "ref hidden under dependentSchemas", body: jsonSchemaResponseFormatBody(`{"dependentSchemas":{"x":{"$ref":"#/y"}}}`), wantErr: ErrSchemaRef},
		{name: "defs hidden under then", body: jsonSchemaResponseFormatBody(`{"then":{"$defs":{"x":{}}}}`), wantErr: ErrSchemaRef},
		// CVE-2025-48944: bad `type` value crashes xgrammar's C++ grammar compiler.
		{name: "bad schema type string", body: jsonSchemaResponseFormatBody(`{"type":"something"}`), wantErr: ErrSchemaType},
		{name: "bad schema type array entry", body: jsonSchemaResponseFormatBody(`{"type":["string","weird"]}`), wantErr: ErrSchemaType},
		{name: "bad schema type bool", body: jsonSchemaResponseFormatBody(`{"type":true}`), wantErr: ErrSchemaType},
		{name: "bad schema type nested", body: jsonSchemaResponseFormatBody(`{"type":"object","properties":{"x":{"type":"not_a_type"}}}`), wantErr: ErrSchemaType},
		// CVE-2025-48944: unclosed regex crashes the regex compiler before vLLM rejects.
		{name: "bad pattern unclosed group", body: jsonSchemaResponseFormatBody(`{"type":"string","pattern":"("}`), wantErr: ErrSchemaPattern},
		{name: "bad pattern unclosed char class", body: jsonSchemaResponseFormatBody(`{"type":"string","pattern":"["}`), wantErr: ErrSchemaPattern},
		{name: "bad pattern not string", body: jsonSchemaResponseFormatBody(`{"type":"string","pattern":42}`), wantErr: ErrSchemaPattern},
		{name: "bad pattern too long", body: jsonSchemaResponseFormatBody(`{"type":"string","pattern":"` + strings.Repeat("a", 513) + `"}`), wantErr: ErrSchemaPattern},
		{name: "bad pattern nested", body: jsonSchemaResponseFormatBody(`{"type":"object","properties":{"x":{"type":"string","pattern":"["}}}`), wantErr: ErrSchemaPattern},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			err := v.Validate(ValidatorContext{Document: doc})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func jsonSchemaResponseFormatBody(schemaJSON string) string {
	return `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + schemaJSON + `}}}`
}

func nestedPropertiesSchema(depth int) string {
	if depth <= 1 {
		return `{"type":"object"}`
	}
	return `{"type":"object","properties":{"x":` + nestedPropertiesSchema(depth-1) + `}}`
}

func nestedIfSchema(depth int) string {
	return nestedKeywordSchema("if", depth)
}

// nestedKeywordSchema produces a chain of `depth` schemas nested through the given JSON-Schema
// keyword. Used to assert the walker enters every schema-valued keyword, not just the ones
// it explicitly recognized in its early implementation.
func nestedKeywordSchema(keyword string, depth int) string {
	if depth <= 1 {
		return `{"type":"object"}`
	}
	return `{"` + keyword + `":` + nestedKeywordSchema(keyword, depth-1) + `}`
}

func manyPropertiesSchema(count int) string {
	var b strings.Builder
	b.WriteString(`{"properties":{`)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":{}`)
	}
	b.WriteString(`}}`)
	return b.String()
}

// coBoundarySchema constructs a schema that sits at both the depth and node-count limit
// simultaneously: a single-property chain of `chainDepth` levels, with `leafProps` empty
// sibling properties at the deepest level. Total nodes = chainDepth + leafProps; depth =
// chainDepth + 1 (leaves are one step deeper than the chain).
func coBoundarySchema(chainDepth, leafProps int) string {
	var b strings.Builder
	for i := 0; i < chainDepth-1; i++ {
		b.WriteString(`{"type":"object","properties":{"a":`)
	}
	b.WriteString(`{"type":"object","properties":{`)
	for i := 0; i < leafProps; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"p`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":{}`)
	}
	b.WriteString(`}}`)
	for i := 0; i < chainDepth-1; i++ {
		b.WriteString(`}}`)
	}
	return b.String()
}

// schemaOfMarshalledSize returns a JSON-Schema map whose post-Marshal byte length equals
// `want`. Used to exercise the CheckSize boundary without guessing wrapper overhead.
func schemaOfMarshalledSize(tb testing.TB, want int) string {
	tb.Helper()
	build := func(padLen int) []byte {
		s := `{"type":"string","description":"` + strings.Repeat("a", padLen) + `"}`
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			tb.Fatalf("unmarshal: %v", err)
		}
		out, err := json.Marshal(m)
		if err != nil {
			tb.Fatalf("marshal: %v", err)
		}
		return out
	}
	for padLen := want - 64; padLen < want+64; padLen++ {
		if padLen < 0 {
			continue
		}
		if len(build(padLen)) == want {
			return string(build(padLen))
		}
	}
	tb.Fatalf("could not build schema of marshalled size %d", want)
	return ""
}

func bigEnumSchema(n int) string {
	var b strings.Builder
	b.WriteString(`{"enum":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(i))
	}
	b.WriteString(`]}`)
	return b.String()
}
