package filters

import (
	"strings"
	"testing"
)

const (
	patternPropertiesKeyError = "schema patternProperties key is not a valid regular expression: "
	maxPatternPropertiesKey   = 512
)

var patternPropertiesFieldCases = []struct {
	name   string
	field  func(t *testing.T, schema map[string]any) string
	prefix string
}{
	{"tools", func(t *testing.T, schema map[string]any) string {
		return `,"tools":[` + toolWithParams(t, schema) + `]`
	}, "tools[0].function.parameters: "},
	{"response_format", func(t *testing.T, schema map[string]any) string {
		return responseFormatField(t, schema)
	}, "response_format.json_schema.schema: "},
	{"structured_outputs.json", func(t *testing.T, schema map[string]any) string {
		return structuredOutputsField(t, "json", schema)
	}, "structured_outputs.json: "},
}

func schemaWithPatternPropertyKeys(patterns []string, keys ...string) map[string]any {
	schema := schemaWithPatterns(patterns...)
	children := make(map[string]any, len(keys))
	for _, key := range keys {
		children[key] = map[string]any{"type": "string"}
	}
	schema["patternProperties"] = children
	return schema
}

// Test flow:
//  1. For tools, response_format and structured_outputs.json, put one patternProperties key of exactly 512 bytes in a schema and call NormalizeRequest.
//  2. Assert the request is accepted at the cap.
//  3. Repeat with a 513-byte key and assert a 400 naming the field, the patternProperties key and the length limit.
func TestNormalizeRequestCapsThePatternPropertiesKeyLength(t *testing.T) {
	for _, fixture := range patternPropertiesFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			atCap := schemaWithPatternPropertyKeys(nil, fillerPattern("k", maxPatternPropertiesKey))
			if _, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, atCap))); err != nil {
				t.Fatalf("NormalizeRequest() at the cap = %v, want acceptance", err)
			}
			over := schemaWithPatternPropertyKeys(nil, fillerPattern("k", maxPatternPropertiesKey+1))
			_, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, over)))
			requireToolsRejection(t, err, fixture.prefix+patternPropertiesKeyError+"length 513 exceeds limit 512")
		})
	}
}

// Test flow:
//  1. For tools, response_format and structured_outputs.json, spend 3585 of the 4096 budget bytes on `pattern` values in a schema.
//  2. Add a distinct 511-byte patternProperties key and call NormalizeRequest; assert the request is accepted, because it sits exactly at the limit.
//  3. Repeat with a 512-byte key and assert a 400 whose message names the field and the pattern budget.
func TestNormalizeRequestChargesPatternPropertiesKeysToThePatternBudget(t *testing.T) {
	spent := spentToolsPatterns()
	for _, fixture := range patternPropertiesFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			atLimit := schemaWithPatternPropertyKeys(spent, fillerPattern("k", 511))
			if _, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, atLimit))); err != nil {
				t.Fatalf("NormalizeRequest() at the limit = %v, want acceptance", err)
			}
			over := schemaWithPatternPropertyKeys(spent, fillerPattern("k", 512))
			_, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, over)))
			requireToolsRejection(t, err, fixture.prefix+patternBudgetExceeded)
		})
	}
}

// Test flow:
//  1. For tools, response_format and structured_outputs.json, spend the whole 4096 byte budget on eight distinct `pattern` values, then add patternProperties keys equal to two of them.
//  2. Call NormalizeRequest and assert the request is accepted, since a pattern already seen is not charged again.
//  3. Replace one key with a distinct 1-byte key and assert a 400 with the pattern budget error, so the dedup is the only reason the first request fit.
func TestNormalizeRequestChargesAPatternPropertiesKeyEqualToAPatternOnce(t *testing.T) {
	patterns := distinctFillerPatterns("s", 8, toolsMaxPatternLen)
	for _, fixture := range patternPropertiesFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			repeated := schemaWithPatternPropertyKeys(patterns, patterns[0], patterns[7])
			if _, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, repeated))); err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
			distinct := schemaWithPatternPropertyKeys(patterns, patterns[0], "b")
			_, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, distinct)))
			requireToolsRejection(t, err, fixture.prefix+patternBudgetExceeded)
		})
	}
}

// Test flow:
//  1. For tools, response_format and structured_outputs.json, nest a schema under `properties` whose patternProperties holds the key `(unclosed`.
//  2. Call NormalizeRequest.
//  3. Assert a 400 naming the field and the patternProperties key, with the regular-expression parse error, so a key nested under `properties` is checked.
func TestNormalizeRequestRejectsAPatternPropertiesKeyWithInvalidSyntax(t *testing.T) {
	for _, fixture := range patternPropertiesFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{"labels": schemaWithPatternPropertyKeys(nil, "(unclosed")}}
			_, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, schema)))
			requireToolsRejection(t, err, fixture.prefix+patternPropertiesKeyError+"error parsing regexp")
		})
	}
}

// Test flow:
//  1. Build a tools schema whose patternProperties holds the invalid keys `(c`, `(a` and `(b`.
//  2. Call NormalizeRequest thirty times.
//  3. Assert every rejection names `(a`, the first key in sorted order, so the 400 text does not depend on map iteration order.
func TestNormalizeRequestNamesTheFirstSortedInvalidPatternPropertiesKey(t *testing.T) {
	body := patternRequestBody(nil, `,"tools":[`+toolWithParams(t, schemaWithPatternPropertyKeys(nil, "(c", "(a", "(b"))+`]`)
	for range 30 {
		_, err := normalizeToolsTestRequest(t, body)
		requireToolsRejection(t, err, "`(a`")
	}
}

// Test flow:
//  1. For tools, response_format and structured_outputs.json, build a schema with the five realistic `pattern` values and a nested object whose patternProperties holds three short keys, one of them a repeat of a `pattern`.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted.
func TestNormalizeRequestAcceptsARealisticSchemaWithPatternProperties(t *testing.T) {
	keys := []string{`^x-[a-z]+$`, `^[a-z0-9_]+$`, realisticPatterns[0]}
	for _, fixture := range patternPropertiesFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			schema := schemaWithPatterns(realisticPatterns...)
			schema["properties"].(map[string]any)["labels"] = schemaWithPatternPropertyKeys(nil, keys...)
			if _, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, schema))); err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
		})
	}
}

// Test flow:
//  1. Build a schema whose patternProperties keys are `(unclosed` and a 513-byte run of one letter, and run `SchemaBounds.Check` with a pattern cap of 0.
//  2. Assert the check accepts it, because a cap of 0 turns the pattern rules off for keys as it does for `pattern`.
func TestSchemaBoundsPatternCapOfZeroSkipsPatternPropertiesKeys(t *testing.T) {
	bounds := testToolsBounds()
	bounds.MaxPatternLen = 0
	schema := schemaWithPatternPropertyKeys(nil, "(unclosed", strings.Repeat("a", maxPatternPropertiesKey+1))
	if err := bounds.Check(schema); err != nil {
		t.Fatalf("Check() = %v, want acceptance with the pattern rules off", err)
	}
}
