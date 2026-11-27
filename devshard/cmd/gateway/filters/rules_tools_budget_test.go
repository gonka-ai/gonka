package filters

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

const (
	budgetFillerTools        = 40
	budgetFillerPatternBytes = 100
	budgetFillerTotalBytes   = budgetFillerTools * budgetFillerPatternBytes
	budgetCrossingToolPath   = "tools[40].function.parameters: pattern budget exceeded: limit 4096 bytes"
	distinctAgentTools       = 40
	distinctAgentPatternSize = 30
	unclosedPatternPrefix    = "(unclosed"
)

func toolsBudgetBody(tools []string) string {
	return `{"messages":[{"role":"user","content":"hi"}],"tools":[` + strings.Join(tools, ",") + `]}`
}

func repeatedTools(count int, tool string) []string {
	tools := make([]string, count)
	for index := range tools {
		tools[index] = tool
	}
	return tools
}

func wideToolSchema(properties int) map[string]any {
	children := make(map[string]any, properties)
	for index := range properties {
		children["p"+strconv.Itoa(index)] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": children}
}

func patternSchema(pattern string) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string", "pattern": pattern}}}
}

func requireToolsRejection(t *testing.T, err error, want string) {
	t.Helper()
	var rejection *RejectError
	if !errors.As(err, &rejection) {
		t.Fatalf("NormalizeRequest() = %v, want a *RejectError", err)
	}
	if rejection.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rejection.Status, http.StatusBadRequest)
	}
	if !strings.Contains(rejection.Message, want) {
		t.Fatalf("message = %q, want it to contain %q", rejection.Message, want)
	}
}

func benchmarkBody(b testing.TB, tools []map[string]any) []byte {
	b.Helper()
	items := make([]any, len(tools))
	for index, schema := range tools {
		items[index] = map[string]any{"type": "function", "function": map[string]any{"name": "tool_" + strconv.Itoa(index), "description": "d", "parameters": schema}}
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "tools": items})
	if err != nil {
		b.Fatalf("json.Marshal: %v", err)
	}
	return body
}

var realisticPatterns = []string{`^[a-z0-9-]+$`, `^\d{4}-\d{2}-\d{2}$`, `^[A-Za-z0-9_-]{1,64}$`, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`}

func realisticTool(index, nodes, patterns int) map[string]any {
	properties := make(map[string]any)
	remaining := nodes - 1
	for position := 0; remaining > 0; position++ {
		name := "field_" + strconv.Itoa(position)
		switch position % 5 {
		case 0:
			properties[name] = map[string]any{"type": "string", "description": "A free text field that the model fills in for this call."}
			remaining--
		case 1:
			properties[name] = map[string]any{"type": "string", "enum": []any{"low", "medium", "high"}}
			remaining--
		case 2:
			properties[name] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "value": map[string]any{"type": "number"}}}}
			remaining -= 4
		case 3:
			properties[name] = map[string]any{"type": "integer", "minimum": 0, "maximum": 1000}
			remaining--
		default:
			properties[name] = map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "boolean"}, "b": map[string]any{"type": "string"}}}
			remaining -= 3
		}
	}
	for position := range patterns {
		properties["pattern_"+strconv.Itoa(position)] = map[string]any{"type": "string", "pattern": realisticPatterns[(index+position)%len(realisticPatterns)]}
	}
	return map[string]any{"type": "object", "properties": properties, "required": []any{"field_0"}}
}

func realisticTools(count, uniformNodes, patternEvery int) []map[string]any {
	tools := make([]map[string]any, count)
	for index := range tools {
		patterns := 0
		if patternEvery > 0 && index%patternEvery == 0 {
			patterns = 1
		}
		nodes := uniformNodes
		if nodes == 0 {
			nodes = 10 + (index*17)%71
		}
		tools[index] = realisticTool(index, nodes, patterns)
	}
	return tools
}

// Test flow:
//  1. Call NormalizeRequest with 129 small tools.
//  2. Assert the request is rejected with a 400 naming the 128-entry limit.
func TestNormalizeRequestRejectsMoreThanMaxTools(t *testing.T) {
	body := toolsBudgetBody(repeatedTools(toolsMaxCount+1, `{"type":"function","function":{"name":"x"}}`))
	_, err := normalizeToolsTestRequest(t, body)
	requireToolsRejection(t, err, "tools: array length 129 exceeds limit 128")
}

// Test flow:
//  1. Call NormalizeRequest with exactly 128 small tools that each carry a parameters object.
//  2. Assert the request is accepted and all 128 tools are forwarded.
func TestNormalizeRequestAcceptsExactlyMaxTools(t *testing.T) {
	body := toolsBudgetBody(repeatedTools(toolsMaxCount, toolWithParams(t, map[string]any{"type": "object"})))
	document, err := normalizeToolsTestRequest(t, body)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	tools, _ := document.Get("tools")
	if got := len(tools.([]any)); got != toolsMaxCount {
		t.Fatalf("len(tools) = %d, want %d", got, toolsMaxCount)
	}
}

// Test flow:
//  1. Build 128 tools whose parameters each carry exactly the per-tool node limit of 256 nodes.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted and keeps all 128 tools, proving no node total is enforced across tools.
func TestNormalizeRequestAcceptsMaxCountToolsAtThePerToolNodeLimit(t *testing.T) {
	schema := wideToolSchema(toolsMaxNodes - 1)
	if err := testToolsBounds().Check(schema); err != nil {
		t.Fatalf("a single tool must be within bounds, Check() = %v", err)
	}
	document, err := normalizeToolsTestRequest(t, toolsBudgetBody(repeatedTools(toolsMaxCount, toolWithParams(t, schema))))
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	tools, _ := document.Get("tools")
	if got := len(tools.([]any)); got != toolsMaxCount {
		t.Fatalf("len(tools) = %d, want %d", got, toolsMaxCount)
	}
}

func distinctPatternTools(t *testing.T, patternLengths ...int) []string {
	t.Helper()
	tools := make([]string, len(patternLengths))
	for index, length := range patternLengths {
		prefix := "^" + strconv.Itoa(index) + "_"
		tools[index] = toolWithParams(t, patternSchema(prefix+strings.Repeat("a", length-len(prefix))))
	}
	return tools
}

func repeatedLengths(count, length int) []int {
	lengths := make([]int, count)
	for index := range lengths {
		lengths[index] = length
	}
	return lengths
}

// Test flow:
//  1. Build 40 tools with a distinct 100-byte pattern each, then a 41st whose distinct invalid pattern crosses the 4096-byte budget.
//  2. Call NormalizeRequest.
//  3. Assert the request is rejected with the pattern budget error on the 41st tool and not with a regular-expression error, proving the budget is charged before the pattern is parsed.
func TestNormalizeRequestRejectsToolsOverTheTotalPatternBudget(t *testing.T) {
	tools := distinctPatternTools(t, repeatedLengths(budgetFillerTools, budgetFillerPatternBytes)...)
	crossing := unclosedPatternPrefix + strings.Repeat("a", budgetFillerPatternBytes-len(unclosedPatternPrefix))
	tools = append(tools, toolWithParams(t, patternSchema(crossing)))
	_, err := normalizeToolsTestRequest(t, toolsBudgetBody(tools))
	requireToolsRejection(t, err, budgetCrossingToolPath)
}

// Test flow:
//  1. Build distinct patterns that sum to exactly the 4096-byte budget, and a copy with one more byte.
//  2. Call NormalizeRequest twice with the exact body, so a second request proves the budget starts fresh.
//  3. Assert the exact body is accepted both times and the one-byte-over body is rejected with the pattern budget error.
func TestNormalizeRequestPatternBudgetBoundary(t *testing.T) {
	lengths := append(repeatedLengths(budgetFillerTools, budgetFillerPatternBytes), requestPatternMaxBytes-budgetFillerTotalBytes)
	exact := toolsBudgetBody(distinctPatternTools(t, lengths...))
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := normalizeToolsTestRequest(t, exact); err != nil {
			t.Fatalf("NormalizeRequest() attempt %d = %v, want acceptance", attempt, err)
		}
	}
	lengths[len(lengths)-1]++
	_, err := normalizeToolsTestRequest(t, toolsBudgetBody(distinctPatternTools(t, lengths...)))
	requireToolsRejection(t, err, budgetCrossingToolPath)
}

func heavyPattern(arm string, arms int) string {
	return "(" + strings.Repeat("(|"+arm+")", arms) + "){1000}"
}

// Test flow:
//  1. Build eight distinct worst-shape patterns of 512 bytes, which together spend exactly the 4096-byte budget.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted, so the budget does not reject the heaviest request it is sized to allow.
func TestNormalizeRequestAcceptsTheHeaviestPatternsWithinTheBudget(t *testing.T) {
	patterns := make([]string, 0, 8)
	for arm := range strings.SplitSeq("abcdefgh", "") {
		patterns = append(patterns, heavyPattern(arm, 126))
	}
	total := 0
	tools := make([]string, len(patterns))
	for index, pattern := range patterns {
		total += len(pattern)
		tools[index] = toolWithParams(t, patternSchema(pattern))
	}
	if total != requestPatternMaxBytes {
		t.Fatalf("patterns total %d bytes, want exactly the budget %d", total, requestPatternMaxBytes)
	}
	if _, err := normalizeToolsTestRequest(t, toolsBudgetBody(tools)); err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
}

// Test flow:
//  1. Build 128 tools that all carry the same 200-byte pattern, which would total 25600 bytes if each were charged.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted, since a pattern already seen in the request is not charged again.
func TestNormalizeRequestChargesARepeatedPatternOnce(t *testing.T) {
	body := toolsBudgetBody(repeatedTools(toolsMaxCount, toolWithParams(t, patternSchema("^"+strings.Repeat("a", 198)+"$"))))
	if _, err := normalizeToolsTestRequest(t, body); err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
}

// Test flow:
//  1. Build agent-sized requests from the benchmark fixtures: 60 tools with 10 to 80 nodes and a pattern on every sixth, 80 tools with a pattern each, and 80 tools with 80 nodes each.
//  2. Call NormalizeRequest on each.
//  3. Assert every request is accepted and keeps all its tools.
func TestNormalizeRequestAcceptsAgentSizedRequests(t *testing.T) {
	for _, fixture := range []struct {
		name                       string
		count, nodes, patternEvery int
	}{
		{"sixty tools with a pattern on every sixth", 60, 0, 6},
		{"eighty tools with a pattern each", 80, 0, 1},
		{"eighty tools of eighty nodes", 80, 80, 8},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			document, err := normalizeToolsTestRequest(t, string(benchmarkBody(t, realisticTools(fixture.count, fixture.nodes, fixture.patternEvery))))
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
			tools, _ := document.Get("tools")
			if got := len(tools.([]any)); got != fixture.count {
				t.Fatalf("len(tools) = %d, want %d", got, fixture.count)
			}
		})
	}
}

// Test flow:
//  1. Build 40 tools that each carry a different 30-byte pattern, which spends 1200 of the 4096 budget bytes because no pattern repeats.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted and keeps all 40 tools.
func TestNormalizeRequestAcceptsDistinctPatternsAtAgentScale(t *testing.T) {
	tools := distinctPatternTools(t, repeatedLengths(distinctAgentTools, distinctAgentPatternSize)...)
	document, err := normalizeToolsTestRequest(t, toolsBudgetBody(tools))
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	got, _ := document.Get("tools")
	if len(got.([]any)) != distinctAgentTools {
		t.Fatalf("len(tools) = %d, want %d", len(got.([]any)), distinctAgentTools)
	}
}

// Test flow:
//  1. Run `validTools` with a zero MaxCount against more tools than the default count limit allows.
//  2. Assert the rule accepts them, proving a zero limit turns the count cap off rather than rejecting everything.
func TestValidToolsZeroMaxCountDisablesTheCountBound(t *testing.T) {
	rule := validTools(toolsBounds{SchemaBounds: testToolsBounds().SchemaBounds}, "auto")
	tools := repeatedTools(toolsMaxCount+1, `{"type":"function","function":{"name":"x"}}`)
	document := parseTestDocument(t, `{"tools":[`+strings.Join(tools, ",")+`]}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want acceptance", err)
	}
}

// Test flow:
//  1. Build an ordinary request of five tools, each with nested properties, an enum and a short pattern.
//  2. Call NormalizeRequest.
//  3. Assert the request is accepted and keeps its five tools.
func TestNormalizeRequestAcceptsAFewToolsWithPatterns(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "pattern": "^[a-z]{3}-[0-9]{4}$"},
			"unit":  map[string]any{"type": "string", "enum": []any{"c", "f"}},
			"where": map[string]any{"type": "object", "properties": map[string]any{"zip": map[string]any{"type": "string", "pattern": "^[0-9]{5}$"}}},
		},
		"required": []any{"id"},
	}
	body := toolsBudgetBody(repeatedTools(5, toolWithParams(t, schema)))
	document, err := normalizeToolsTestRequest(t, body)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	tools, _ := document.Get("tools")
	if got := len(tools.([]any)); got != 5 {
		t.Fatalf("len(tools) = %d, want 5", got)
	}
}
