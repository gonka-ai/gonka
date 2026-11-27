package filters

import (
	"strconv"
	"strings"
	"testing"
)

func maxNodesSchema(b *testing.B) map[string]any {
	b.Helper()
	properties := make(map[string]any, toolsMaxNodes-1)
	for index := range toolsMaxNodes - 1 {
		properties["prop_"+strconv.Itoa(index)] = map[string]any{"type": "string", "description": strings.Repeat("x", 200)}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	size, err := jsonMarshaledSize(schema)
	if err != nil || size > toolsMaxSizeBytes {
		b.Fatalf("fixture size = %d, err = %v, want within %d", size, err, toolsMaxSizeBytes)
	}
	return schema
}

func maxDepthSchema(b *testing.B) map[string]any {
	b.Helper()
	schema := map[string]any{"type": "string"}
	for range toolsMaxDepth - 1 {
		schema = map[string]any{"type": "array", "items": schema}
	}
	return schema
}

func BenchmarkToolsLargestRequestWithoutPatterns(b *testing.B) {
	bounds := testToolsBounds().SchemaBounds
	for name, schema := range map[string]map[string]any{"wide_256_nodes": maxNodesSchema(b), "deep_16_levels": maxDepthSchema(b)} {
		parameters := make([]map[string]any, toolsMaxCount)
		for index := range parameters {
			parameters[index] = schema
		}
		b.Run("check_only/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, parameter := range parameters {
					if err := bounds.checkWithin(parameter, nil); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run("walk_only/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, parameter := range parameters {
					var nodes int
					if err := bounds.walk(parameter, 1, &nodes, nil); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run("size_only/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, parameter := range parameters {
					if err := checkSize(parameter, bounds.MaxSizeBytes); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		body := benchmarkBody(b, parameters)
		b.Run("normalize_request/"+name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := NormalizeRequest(body, Options{DefaultMaxTokens: 3072, MaxTokensCap: 4096}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func padPattern(unit string) string {
	return strings.Repeat(unit, toolsMaxPatternLen/len(unit))
}

func BenchmarkPatternSyntaxWorstCase(b *testing.B) {
	shapes := []struct{ name, pattern string }{
		{"concat_a_1000", padPattern("a{1000}")},
		{"concat_dot_1000", padPattern(".{1000}")},
		{"concat_nested_30x30", padPattern("(a{30}){30}")},
		{"concat_group_opt_1000", padPattern("((a?)(b?)){1000}")},
		{"concat_deep_empty_arms_1000", padPattern("((((|a)|b)|c)|d){1000}")},
		{"one_body_optionals_1000", "(" + strings.Repeat("a?", (toolsMaxPatternLen-8)/2) + "){1000}"},
		{"one_body_empty_arms_1000", "(" + strings.Repeat("(|a)", (toolsMaxPatternLen-8)/4) + "){1000}"},
		{"folded_negated_classes", strings.Repeat(`(?i)[^\p{Lu}\p{Ll}]`, 24)},
		{"alternated_unicode_classes", strings.Repeat(`[\p{L}]|`, 60) + "x"},
	}
	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			if len(shape.pattern) > toolsMaxPatternLen {
				b.Fatalf("pattern is %d bytes, limit %d", len(shape.pattern), toolsMaxPatternLen)
			}
			if err := testToolsBounds().validateSchemaPattern(map[string]any{"pattern": shape.pattern}, nil); err != nil {
				b.Fatalf("per-pattern checks reject it: %v", err)
			}
			b.ReportMetric(float64(len(shape.pattern)), "pattern_bytes")
			b.ReportAllocs()
			for b.Loop() {
				if err := validateRegexSyntax(shape.pattern); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPatternSyntaxShort(b *testing.B) {
	for _, pattern := range []string{"a", `^[a-z0-9-]+$`, `^\d{4}-\d{2}-\d{2}$`, `^[A-Za-z0-9_-]{1,64}$`, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`} {
		b.Run(pattern, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := validateRegexSyntax(pattern); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRealisticAgentRequest(b *testing.B) {
	for _, fixture := range []struct {
		name         string
		count        int
		nodes        int
		patternEvery int
	}{
		{"tools40_nodes10to80_patterns0", 40, 0, 0},
		{"tools60_nodes10to80_patterns10", 60, 0, 6},
		{"tools80_nodes10to80_patterns40", 80, 0, 2},
		{"tools80_nodes10to80_patterns80", 80, 0, 1},
		{"tools80_nodes80_patterns10", 80, 80, 8},
	} {
		tools := realisticTools(fixture.count, fixture.nodes, fixture.patternEvery)
		body := benchmarkBody(b, tools)
		b.Run(fixture.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := NormalizeRequest(body, Options{DefaultMaxTokens: 3072, MaxTokensCap: 4096}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWorstPatternRequest(b *testing.B) {
	shapes := map[string]func(tag string) string{
		"repeated_empty_arms": func(tag string) string { return heavyPattern(tag, 126) },
		"folded_negated_classes": func(tag string) string {
			return strings.Repeat(`(?i)[^\p{Lu}\p{Ll}]`, 24) + tag
		},
		"alternated_unicode_classes": func(tag string) string { return strings.Repeat(`[\p{L}]|`, 60) + tag },
	}
	tags := strings.Split("abcdefgh", "")
	for name, shape := range shapes {
		patterns := make([]string, len(tags))
		total := 0
		for index, tag := range tags {
			patterns[index] = shape(tag)
			total += len(patterns[index])
		}
		if total > requestPatternMaxBytes {
			b.Fatalf("%s patterns total %d bytes, budget %d", name, total, requestPatternMaxBytes)
		}
		tools := make([]string, len(patterns))
		for index, pattern := range patterns {
			tools[index] = `{"type":"function","function":{"name":"t` + strconv.Itoa(index) + `","parameters":` + mustMarshalJSON(b, patternSchema(pattern)) + `}}`
		}
		variants := map[string]string{
			"tools":           patternRequestBody(tools, ""),
			"response_format": patternRequestBody(nil, responseFormatField(b, schemaWithPatterns(patterns...))),
		}
		for variant, body := range variants {
			b.Run(name+"/"+variant, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := NormalizeRequest([]byte(body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 4096}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
