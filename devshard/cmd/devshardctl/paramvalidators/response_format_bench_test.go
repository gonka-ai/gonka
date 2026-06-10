package paramvalidators

import (
	"strings"
	"testing"
)

// benchValidator returns the production-tuned validator. Reused across benchmarks so we
// measure the actual configuration that ships, not a synthetic one. Keep these constants
// in sync with the catalog entry in cmd/devshardctl/request_filters_parameters.go.
func benchValidator() ResponseFormatValidator {
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

func BenchmarkResponseFormatValidator_Absent(b *testing.B) {
	v := benchValidator()
	doc := parseDocument(b, `{"messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResponseFormatValidator_TypeText(b *testing.B) {
	v := benchValidator()
	doc := parseDocument(b, `{"response_format":{"type":"text"},"messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResponseFormatValidator_TypeJSONObject(b *testing.B) {
	v := benchValidator()
	doc := parseDocument(b, `{"response_format":{"type":"json_object"},"messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResponseFormatValidator_SimpleSchema(b *testing.B) {
	v := benchValidator()
	doc := parseDocument(b, `{"response_format":{"type":"json_schema","json_schema":{"name":"weather_v1","schema":{"type":"object","properties":{"city":{"type":"string"},"temp":{"type":"number"}},"required":["city"]}}},"messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResponseFormatValidator_AtLimits exercises the worst-case ACCEPTED schema:
// hits MaxDepth=16 and MaxNodes=128 simultaneously via a 15-level property chain with 113
// leaf siblings at the deepest level (15 + 113 = 128 nodes, leaves at depth 16).
func BenchmarkResponseFormatValidator_AtLimits(b *testing.B) {
	v := benchValidator()
	schema := buildLimitsSchema()
	body := `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + schema + `}},"messages":[{"role":"user","content":"hello"}]}`
	doc := parseDocument(b, body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResponseFormatValidator_RejectsRecursion measures the fast-reject path on the
// pathological case that motivated the validator. The depth check must fire early at depth 6.
func BenchmarkResponseFormatValidator_RejectsRecursion(b *testing.B) {
	v := benchValidator()
	deep := `{"type":"object"}`
	for i := 0; i < 200; i++ {
		deep = `{"type":"object","properties":{"x":` + deep + `}}`
	}
	body := `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + deep + `}},"messages":[{"role":"user","content":"hello"}]}`
	doc := parseDocument(b, body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err == nil {
			b.Fatal("expected reject")
		}
	}
}

// BenchmarkResponseFormatValidator_RejectsOversized measures the size-gate path.
func BenchmarkResponseFormatValidator_RejectsOversized(b *testing.B) {
	v := benchValidator()
	big := `{"type":"object","properties":{"` + strings.Repeat("a", 17*1024) + `":{"type":"string"}}}`
	body := `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + big + `}},"messages":[{"role":"user","content":"hello"}]}`
	doc := parseDocument(b, body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Validate(ValidatorContext{Document: doc}); err == nil {
			b.Fatal("expected reject")
		}
	}
}

func buildLimitsSchema() string {
	// 15-level property chain + 113 leaf siblings at the bottom: hits MaxDepth=16 and
	// MaxNodes=128 simultaneously. Shape shared with the unit-test "depth and nodes both at
	// limit" case so bench and test exercise the same boundary.
	return coBoundarySchema(15, 113)
}
