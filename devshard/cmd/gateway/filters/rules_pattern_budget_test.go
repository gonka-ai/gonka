package filters

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	patternBudgetExceeded = "pattern budget exceeded: limit 4096 bytes"
	parseAllocationsFloor = 10
	allocationRuns        = 20
)

func fillerPattern(tag string, length int) string {
	prefix := "^" + tag + "_"
	return prefix + strings.Repeat("a", length-len(prefix))
}

func distinctFillerPatterns(tag string, count, length int) []string {
	patterns := make([]string, count)
	for index := range patterns {
		patterns[index] = fillerPattern(tag+strconv.Itoa(index), length)
	}
	return patterns
}

func schemaWithPatterns(patterns ...string) map[string]any {
	properties := make(map[string]any, len(patterns))
	for index, pattern := range patterns {
		properties["v"+strconv.Itoa(index)] = map[string]any{"type": "string", "pattern": pattern}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func toolsWithPatterns(t *testing.T, patterns ...string) []string {
	t.Helper()
	tools := make([]string, len(patterns))
	for index, pattern := range patterns {
		tools[index] = toolWithParams(t, patternSchema(pattern))
	}
	return tools
}

func patternRequestBody(tools []string, extra string) string {
	body := `{"messages":[{"role":"user","content":"hi"}]`
	if len(tools) > 0 {
		body += `,"tools":[` + strings.Join(tools, ",") + `]`
	}
	return body + extra + `}`
}

func responseFormatField(t testing.TB, schema map[string]any) string {
	t.Helper()
	return `,"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + mustMarshalJSON(t, schema) + `}}`
}

func structuredOutputsField(t *testing.T, key string, value any) string {
	t.Helper()
	return `,"structured_outputs":{"` + key + `":` + mustMarshalJSON(t, value) + `}`
}

var schemaFieldCases = []struct {
	name   string
	field  func(t *testing.T, patterns ...string) string
	prefix string
}{
	{"response_format", func(t *testing.T, patterns ...string) string {
		return responseFormatField(t, schemaWithPatterns(patterns...))
	}, "response_format.json_schema.schema: "},
	{"structured_outputs.json", func(t *testing.T, patterns ...string) string {
		return structuredOutputsField(t, "json", schemaWithPatterns(patterns...))
	}, "structured_outputs.json: "},
}

var singlePatternFieldCases = []struct {
	name   string
	field  func(t *testing.T, pattern string) string
	prefix string
}{
	{"structured_outputs.regex", func(t *testing.T, pattern string) string {
		return structuredOutputsField(t, "regex", pattern)
	}, "structured_outputs.regex: "},
	{"structured_outputs.whitespace_pattern", func(t *testing.T, pattern string) string {
		return `,"structured_outputs":{"json_object":true,"whitespace_pattern":` + mustMarshalJSON(t, pattern) + `}`
	}, "structured_outputs.whitespace_pattern: "},
}

func spentToolsPatterns() []string {
	spent := distinctFillerPatterns("t", 7, toolsMaxPatternLen)
	return append(spent, "a")
}

// Test flow:
//  1. Spend 3585 of the 4096 budget bytes on tools, then put one distinct 511-byte pattern in each of response_format and structured_outputs.json.
//  2. Call NormalizeRequest and assert the request is accepted, because it sits exactly at the limit.
//  3. Repeat with a 512-byte pattern and assert a 400 whose message names the field and the pattern budget.
func TestNormalizeRequestSharesThePatternBudgetWithSchemaFields(t *testing.T) {
	tools := toolsWithPatterns(t, spentToolsPatterns()...)
	for _, fixture := range schemaFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			atLimit := patternRequestBody(tools, fixture.field(t, fillerPattern("f", 511)))
			if _, err := normalizeToolsTestRequest(t, atLimit); err != nil {
				t.Fatalf("NormalizeRequest() at the limit = %v, want acceptance", err)
			}
			over := patternRequestBody(tools, fixture.field(t, fillerPattern("f", 512)))
			_, err := normalizeToolsTestRequest(t, over)
			requireToolsRejection(t, err, fixture.prefix+patternBudgetExceeded)
		})
	}
}

// Test flow:
//  1. Spend 3585 of the 4096 budget bytes on tools, then put one distinct 511-byte pattern in structured_outputs.regex and in structured_outputs.whitespace_pattern in turn.
//  2. Call NormalizeRequest and assert the request is accepted, because it sits exactly at the limit.
//  3. Repeat with a 512-byte pattern and assert a 400 whose message names the field and the pattern budget.
func TestNormalizeRequestSharesThePatternBudgetWithStructuredOutputsPatterns(t *testing.T) {
	tools := toolsWithPatterns(t, spentToolsPatterns()...)
	for _, fixture := range singlePatternFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			atLimit := patternRequestBody(tools, fixture.field(t, fillerPattern("f", 511)))
			if _, err := normalizeToolsTestRequest(t, atLimit); err != nil {
				t.Fatalf("NormalizeRequest() at the limit = %v, want acceptance", err)
			}
			over := patternRequestBody(tools, fixture.field(t, fillerPattern("f", 512)))
			_, err := normalizeToolsTestRequest(t, over)
			requireToolsRejection(t, err, fixture.prefix+patternBudgetExceeded)
		})
	}
}

// Test flow:
//  1. Put nine distinct 512-byte patterns, 4608 bytes in all, in a single response_format schema and in a single structured_outputs.json schema, with no tools.
//  2. Call NormalizeRequest on each.
//  3. Assert both are rejected with the pattern budget error, so a field alone is bounded by the budget.
func TestNormalizeRequestBoundsASchemaFieldOnItsOwn(t *testing.T) {
	patterns := distinctFillerPatterns("p", 9, toolsMaxPatternLen)
	for _, fixture := range schemaFieldCases {
		t.Run(fixture.name, func(t *testing.T) {
			_, err := normalizeToolsTestRequest(t, patternRequestBody(nil, fixture.field(t, patterns...)))
			requireToolsRejection(t, err, fixture.prefix+patternBudgetExceeded)
		})
	}
}

// Test flow:
//  1. Put four distinct 512-byte patterns in tools and four more in response_format, each half 2048 bytes and under the limit, 4096 bytes together.
//  2. Call NormalizeRequest and assert the request is accepted.
//  3. Add one more distinct 1-byte pattern to response_format and assert the request is rejected, so the two fields are bounded as a whole.
func TestNormalizeRequestBoundsToolsAndResponseFormatTogether(t *testing.T) {
	toolPatterns := distinctFillerPatterns("t", 4, toolsMaxPatternLen)
	formatPatterns := distinctFillerPatterns("r", 4, toolsMaxPatternLen)
	body := patternRequestBody(toolsWithPatterns(t, toolPatterns...), responseFormatField(t, schemaWithPatterns(formatPatterns...)))
	if _, err := normalizeToolsTestRequest(t, body); err != nil {
		t.Fatalf("NormalizeRequest() at the limit = %v, want acceptance", err)
	}
	formatPatterns = append(formatPatterns, "b")
	body = patternRequestBody(toolsWithPatterns(t, toolPatterns...), responseFormatField(t, schemaWithPatterns(formatPatterns...)))
	_, err := normalizeToolsTestRequest(t, body)
	requireToolsRejection(t, err, "response_format.json_schema.schema: "+patternBudgetExceeded)
}

// Test flow:
//  1. Spend 4096 bytes on eight distinct patterns in tools, then repeat the same eight in response_format and use the first one again as structured_outputs.regex.
//  2. Call NormalizeRequest on the response_format variant and on the regex variant.
//  3. Assert both are accepted, since a pattern already seen anywhere in the request is not charged again.
func TestNormalizeRequestChargesARepeatedPatternOnceAcrossFields(t *testing.T) {
	patterns := distinctFillerPatterns("s", 8, toolsMaxPatternLen)
	tools := toolsWithPatterns(t, patterns...)
	for name, extra := range map[string]string{
		"response_format": responseFormatField(t, schemaWithPatterns(patterns...)),
		"regex":           structuredOutputsField(t, "regex", patterns[0]),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeToolsTestRequest(t, patternRequestBody(tools, extra)); err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
		})
	}
}

// Test flow:
//  1. Build a realistic request: 60 agent tools with short id and date patterns on every sixth, plus a response_format json_schema carrying the five realistic patterns.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted with all 60 tools kept.
func TestNormalizeRequestAcceptsRealisticToolsWithAResponseFormat(t *testing.T) {
	schemas := realisticTools(60, 0, 6)
	tools := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		tools = append(tools, toolWithParams(t, schema))
	}
	body := patternRequestBody(tools, responseFormatField(t, schemaWithPatterns(realisticPatterns...)))
	document, err := normalizeToolsTestRequest(t, body)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	got, _ := document.Get("tools")
	if len(got.([]any)) != 60 {
		t.Fatalf("len(tools) = %d, want 60", len(got.([]any)))
	}
}

// Test flow:
//  1. Admit a pattern into a budget, then run `validateSchemaPattern` on that same pattern with that budget and with a fresh budget, measuring allocations.
//  2. Assert the repeat allocates nothing and leaves the charged bytes unchanged, while the fresh budget allocates and charges the pattern.
//  3. Assert the fresh run allocates at least 10 times, which only a parse of this pattern does, so the zero-allocation repeat was not parsed again.
func TestValidateSchemaPatternDoesNotParseARepeatedPattern(t *testing.T) {
	bounds := testToolsBounds()
	object := map[string]any{"pattern": `^\d{4}-\d{2}-\d{2}$`}
	seen := &patternBudget{limit: defaultPatternBudgetBytes}
	if err := bounds.validateSchemaPattern(object, seen); err != nil {
		t.Fatalf("validateSchemaPattern() = %v, want acceptance", err)
	}
	charged := seen.bytes
	repeat := testing.AllocsPerRun(allocationRuns, func() {
		if err := bounds.validateSchemaPattern(object, seen); err != nil {
			t.Fatalf("validateSchemaPattern() on a repeat = %v, want acceptance", err)
		}
	})
	fresh := testing.AllocsPerRun(allocationRuns, func() {
		if err := bounds.validateSchemaPattern(object, &patternBudget{limit: defaultPatternBudgetBytes}); err != nil {
			t.Fatalf("validateSchemaPattern() on a fresh budget = %v, want acceptance", err)
		}
	})
	if repeat != 0 || fresh < parseAllocationsFloor {
		t.Fatalf("allocations: repeat = %v, fresh = %v, want 0 and at least %d", repeat, fresh, parseAllocationsFloor)
	}
	if seen.bytes != charged {
		t.Fatalf("charged bytes = %d after repeats, want %d", seen.bytes, charged)
	}
}

// Test flow:
//  1. Run a table of valid patterns and of patterns Go rejects at parse time, including repeat counts over the limit and nested repeats too large to expand.
//  2. Compare `validateRegexSyntax` with `regexp.Compile` on each.
//  3. Assert both accept or both reject with identical error text, proving the parse-only check keeps the same accept and reject set.
func TestValidateRegexSyntaxMatchesRegexpCompile(t *testing.T) {
	for _, pattern := range []string{
		``, `a`, `^[a-z0-9-]+$`, `^\d{4}-\d{2}-\d{2}$`, `(?i)[\p{L}\p{N}]+`, `(?P<name>a)(?P<other>b)`, `(?<name>a)`, `a{100}`, `(a{10}){10}`,
		`(`, `)`, `[`, `[z-a]`, `a**`, `*a`, `a{1001}`, `a{2,1}`, `\8`, `(?P<n>a)(?P<n>b)`, `\p{Nope}`, `(?z)`, `x{1000}{1000}`,
		`((a{1000}){1000}){1000}`, strings.Repeat("(", 1001) + "a" + strings.Repeat(")", 1001), `\Qabc`, `(?i)(?s).+`, `[[:alpha:]]`, `[[:nope:]]`, `\pL`, `\x{110000}`,
	} {
		_, compileErr := regexp.Compile(pattern)
		parseErr := validateRegexSyntax(pattern)
		if (compileErr == nil) != (parseErr == nil) {
			t.Errorf("pattern %q: regexp.Compile error = %v, validateRegexSyntax error = %v", pattern, compileErr, parseErr)
			continue
		}
		if compileErr != nil && compileErr.Error() != parseErr.Error() {
			t.Errorf("pattern %q: regexp.Compile error = %q, validateRegexSyntax error = %q", pattern, compileErr, parseErr)
		}
	}
}
