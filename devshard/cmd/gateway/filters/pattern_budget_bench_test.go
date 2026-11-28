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
	bounds := testToolsBounds()
	for name, schema := range map[string]map[string]any{"wide_256_nodes": maxNodesSchema(b), "deep_16_levels": maxDepthSchema(b)} {
		parameters := make([]map[string]any, defaultToolsMaxCount)
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
		if total > defaultPatternBudgetBytes {
			b.Fatalf("%s patterns total %d bytes, budget %d", name, total, defaultPatternBudgetBytes)
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

func ceilingOptions(b *testing.B, toolsCount, patternBytes int) Options {
	b.Helper()
	profile, err := ComposeProfile("m", ModelSetting{Filters: FilterKnobs{ToolsMaxCount: &toolsCount, PatternBudgetBytes: &patternBytes}})
	if err != nil {
		b.Fatalf("ComposeProfile() = %v", err)
	}
	return Options{RoutedModel: "m", Profiles: NewProfileSet(map[string]*Profile{"m": profile}), DefaultMaxTokens: 3072, MaxTokensCap: 4096}
}

func plainConversationBody(b *testing.B) []byte {
	b.Helper()
	const paragraph = `The quick brown fox jumps over the lazy dog, and then explains at some length why it did so, quoting a \"source\" and a path like /usr/local/share for good measure. `
	const messages = 2000
	content := strings.Repeat(paragraph, (MaxBodyBytes-4096)/messages/len(paragraph))
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"}`)
	for range messages {
		body = append(body, `,{"role":"user","content":"`...)
		body = append(body, content...)
		body = append(body, `"}`...)
	}
	return append(body, `]}`...)
}

func plainSingleStringBody() []byte {
	content := strings.Repeat("The quick brown fox jumps over the lazy dog. ", (MaxBodyBytes-1024)/45)
	return []byte(`{"model":"m","messages":[{"role":"user","content":"` + content + `"}]}`)
}

func heavyPatternTools(b *testing.B, budgetBytes int) []string {
	b.Helper()
	var tools []string
	for total, index := 0, 0; ; index++ {
		pattern := strings.Repeat(`(?i)[^\p{Lu}\p{Ll}]`, 24) + strconv.Itoa(index)
		if total += len(pattern); total > budgetBytes {
			return tools
		}
		tools = append(tools, `{"type":"function","function":{"name":"p`+strconv.Itoa(index)+`","parameters":`+mustMarshalJSON(b, patternSchema(pattern))+`}}`)
	}
}

func denseToolsBody(b *testing.B, count, patternBudgetBytes int) []byte {
	b.Helper()
	patternTools := heavyPatternTools(b, patternBudgetBytes)
	build := func(properties int) string {
		dense := `{"type":"function","function":{"name":"d","parameters":` + mustMarshalJSON(b, wideToolSchema(properties)) + `}}`
		tools := append(repeatedTools(count-len(patternTools), dense), patternTools...)
		return patternRequestBody(tools, "")
	}
	low, high := 0, toolsMaxNodes-1
	for low < high {
		middle := (low + high + 1) / 2
		if _, err := ParseDocument([]byte(build(middle))); err == nil {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return []byte(build(low))
}

func BenchmarkRaisedKnobCeilings(b *testing.B) {
	cases := []struct {
		name         string
		body         func(b *testing.B) []byte
		toolsCount   int
		patternBytes int
		rejected     bool
	}{
		{"plain_conversation_10MiB", plainConversationBody, defaultToolsMaxCount, defaultPatternBudgetBytes, false},
		{"plain_single_string_10MiB", func(*testing.B) []byte { return plainSingleStringBody() }, defaultToolsMaxCount, defaultPatternBudgetBytes, false},
		{"defaults_dense_tools_and_heavy_patterns", func(b *testing.B) []byte {
			return denseToolsBody(b, defaultToolsMaxCount, defaultPatternBudgetBytes)
		}, defaultToolsMaxCount, defaultPatternBudgetBytes, false},
		{"ceilings_dense_tools_and_heavy_patterns", func(b *testing.B) []byte {
			return denseToolsBody(b, MaxToolsMaxCount, MaxPatternBudgetBytes)
		}, MaxToolsMaxCount, MaxPatternBudgetBytes, false},
		{"tiny_messages_at_message_cap", func(*testing.B) []byte { return tinyMessagesBody(messagesMaxEntries) }, defaultToolsMaxCount, defaultPatternBudgetBytes, false},
		{"content_parts_at_member_cap", func(*testing.B) []byte { return tinyContentPartsBody(MaxContainerMembers) }, defaultToolsMaxCount, defaultPatternBudgetBytes, false},
		{"wide_object_at_member_cap", func(*testing.B) []byte { return wideObjectBody(MaxContainerMembers - 2) }, defaultToolsMaxCount, defaultPatternBudgetBytes, true},
	}
	for _, testCase := range cases {
		body := testCase.body(b)
		options := ceilingOptions(b, testCase.toolsCount, testCase.patternBytes)
		b.Run(testCase.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				_, err := NormalizeRequest(body, options)
				if (err != nil) != testCase.rejected {
					b.Fatalf("NormalizeRequest() = %v, want rejected = %v", err, testCase.rejected)
				}
			}
		})
	}
}

func tinyMessagesBody(count int) []byte {
	return []byte(`{"model":"m","messages":[` + strings.TrimSuffix(strings.Repeat(`{"role":"user","content":"x"},`, count), ",") + `]}`)
}

func tinyContentPartsBody(count int) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"text","text":"x"},`, count), ",") + `]}]}`)
}

func wideObjectBody(extraKeys int) []byte {
	var body strings.Builder
	body.WriteString(`{"model":"m","messages":[{"role":"user","content":"x"}]`)
	for index := range extraKeys {
		body.WriteString(`,"k` + strconv.Itoa(index) + `":0`)
	}
	body.WriteString(`}`)
	return []byte(body.String())
}
