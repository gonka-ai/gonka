package filters

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"devshard/transport"
)

const (
	bytesPerToken      = 4
	largeContextTokens = 400_000
	hugeContextTokens  = 1_000_000
	requiredHeadroom   = 2.0

	hostCatchUpReserveBytes = 1 << 20
	unboundedSchemaLimit    = 1 << 30
	unboundedDepthLimit     = 1 << 20

	fixtureSeedHigh = 7
	fixtureSeedLow  = 11

	fixtureModel             = "m"
	fixtureMaxTokens         = 1024
	fixtureAgenticMaxTokens  = 4096
	fixtureDefaultMaxTokens  = 3072
	fixtureMaxTokensCap      = 4096
	fixtureSystemPromptBytes = 2_000

	agenticSystemTokens       = 8_000
	agenticMinMessages        = 3_000
	agenticMaxMessages        = 10_000
	fixtureParallelCalls      = 3
	toolSetSize               = 128
	patternedToolEvery        = 4
	longDescriptionEvery      = 16
	longDescriptionBytes      = 4_000
	shortDescriptionMinBytes  = 300
	shortDescriptionVarBytes  = 900
	minPropertiesPerTool      = 4
	extraPropertiesPerTool    = 10
	requiredPropertiesPerTool = 2
	argumentNoteSlackBytes    = 40
	argumentNoteMinBytes      = 4
	callIdentifierModulus     = 500

	userStepMinBytes      = 40
	userStepVarBytes      = 400
	assistantStepMinBytes = 60
	assistantStepVarBytes = 300
	toolStepMinBytes      = 60
	toolStepVarBytes      = 240
	sessionMinSteps       = 4
	sessionVarSteps       = 30
	hugeResultPercent     = 99
	hugeResultMinBytes    = 60_000
	hugeResultVarBytes    = 60_000
	largeResultPercent    = 90
	largeResultMinBytes   = 8_000
	largeResultVarBytes   = 16_000
	mediumResultPercent   = 60
	mediumResultMinBytes  = 1_000
	mediumResultVarBytes  = 3_000
	minimumStepBytes      = 8
	percentScale          = 100

	contentPartsFewer      = 3_000
	contentPartsMore       = 4_000
	imageCount             = 4
	imageBytes             = 200_000
	responseFormatFields   = 40
	responseFormatPatterns = 24
)

var (
	proseWords = strings.Fields("the of and to in is that for it as was with be by on not he this are or his from at which but have an had they you were their one all we can her has there been if more when will would who so no what up out about into than them some could other then its only new these may like use two how our work first well way even want because any day most us")

	codeLines = []string{
		"func (s *Server) handle(w http.ResponseWriter, r *http.Request) {",
		"\tif err := json.NewDecoder(r.Body).Decode(&req); err != nil {",
		"\t\thttp.Error(w, \"bad request: \"+err.Error(), http.StatusBadRequest)",
		"\t\treturn",
		"\t}",
		"    const result = await fetch(`${base}/v1/items?limit=${limit}`, { headers: { \"Authorization\": `Bearer ${token}` } });",
		"src/main/java/com/example/service/OrderService.java:142:    private final Map<String, List<Order>> byCustomer = new HashMap<>();",
		"--- a/internal/pkg/store/store.go\n+++ b/internal/pkg/store/store.go\n@@ -10,7 +10,7 @@ import (",
		"drwxr-xr-x  12 user  staff   384 Nov 27 20:11 node_modules",
		"PASS\nok  \texample.com/pkg/store\t0.412s",
	}

	largeContextOptions = Options{RoutedModel: fixtureModel, DefaultMaxTokens: fixtureDefaultMaxTokens, MaxTokensCap: fixtureMaxTokensCap}
)

type fixtureRandom struct{ source *rand.Rand }

func newFixtureRandom() fixtureRandom {
	return fixtureRandom{source: rand.New(rand.NewPCG(fixtureSeedHigh, fixtureSeedLow))}
}

func (r fixtureRandom) text(bytesWanted int, code bool) string {
	var out strings.Builder
	out.Grow(bytesWanted + 128)
	for out.Len() < bytesWanted {
		if code {
			out.WriteString(codeLines[r.source.IntN(len(codeLines))])
			out.WriteByte('\n')
			continue
		}
		out.WriteString(proseWords[r.source.IntN(len(proseWords))])
		out.WriteByte(' ')
	}
	return out.String()[:bytesWanted]
}

func (r fixtureRandom) propertySchema() map[string]any {
	switch r.source.IntN(5) {
	case 0:
		return map[string]any{"type": "integer", "description": r.text(120, false)}
	case 1:
		return map[string]any{"type": "boolean", "description": r.text(90, false)}
	case 2:
		return map[string]any{"type": "string", "enum": []any{"low", "medium", "high", "auto"}, "description": r.text(100, false)}
	case 3:
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": r.text(150, false)}
	default:
		return map[string]any{"type": "object", "properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": r.text(80, false)},
			"limit": map[string]any{"type": "integer"},
		}, "description": r.text(110, false)}
	}
}

func (r fixtureRandom) toolSet(count int) []any {
	tools := make([]any, 0, count)
	for index := range count {
		properties := map[string]any{}
		required := []any{}
		for prop := range minPropertiesPerTool + r.source.IntN(extraPropertiesPerTool) {
			name := "arg_" + strconv.Itoa(prop)
			properties[name] = r.propertySchema()
			if prop == 0 && index%patternedToolEvery == 0 {
				properties[name] = map[string]any{"type": "string", "pattern": "^tool_" + strconv.Itoa(index) + "_[a-z0-9]{8,32}$"}
			}
			if prop < requiredPropertiesPerTool {
				required = append(required, name)
			}
		}
		descriptionBytes := shortDescriptionMinBytes + r.source.IntN(shortDescriptionVarBytes)
		if index%longDescriptionEvery == 0 {
			descriptionBytes = longDescriptionBytes
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name":        "tool_" + strconv.Itoa(index),
			"description": r.text(descriptionBytes, false),
			"parameters":  map[string]any{"type": "object", "properties": properties, "required": required},
		}})
	}
	return tools
}

func responseFormatPatternSet(count int) []string {
	patterns := make([]string, 0, count)
	for index := range count {
		base := realisticPatterns[index%len(realisticPatterns)]
		patterns = append(patterns, strings.TrimSuffix(base, "$")+"|^x"+strconv.Itoa(index)+"$")
	}
	return patterns
}

func marshalBody(t testing.TB, body map[string]any) []byte {
	t.Helper()
	return []byte(mustMarshalJSON(t, body))
}

func plainMessage(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func hugeUserMessageBody(t testing.TB, tokens int) []byte {
	random := newFixtureRandom()
	return marshalBody(t, map[string]any{"model": fixtureModel, "max_tokens": fixtureMaxTokens, "messages": []any{
		plainMessage("system", random.text(fixtureSystemPromptBytes, false)),
		plainMessage("user", random.text(tokens*bytesPerToken, false)),
	}})
}

type sessionStep struct {
	role    string
	nominal int
	calls   int
}

func (r fixtureRandom) toolResultBytes() int {
	size := toolStepMinBytes + r.source.IntN(toolStepVarBytes)
	switch roll := r.source.IntN(percentScale); {
	case roll >= hugeResultPercent:
		size = hugeResultMinBytes + r.source.IntN(hugeResultVarBytes)
	case roll >= largeResultPercent:
		size = largeResultMinBytes + r.source.IntN(largeResultVarBytes)
	case roll >= mediumResultPercent:
		size = mediumResultMinBytes + r.source.IntN(mediumResultVarBytes)
	}
	return size
}

func (r fixtureRandom) sessionPlan(messages, calls int) []sessionStep {
	plan := make([]sessionStep, 0, messages)
	for len(plan) < messages {
		plan = append(plan, sessionStep{role: "user", nominal: userStepMinBytes + r.source.IntN(userStepVarBytes)})
		for steps := sessionMinSteps + r.source.IntN(sessionVarSteps); steps > 0 && len(plan) < messages-calls; steps-- {
			plan = append(plan, sessionStep{role: "assistant", nominal: assistantStepMinBytes + r.source.IntN(assistantStepVarBytes), calls: calls})
			for range calls {
				plan = append(plan, sessionStep{role: "tool", nominal: r.toolResultBytes()})
			}
		}
	}
	return plan
}

func agenticSessionBody(t testing.TB, messages, calls, tokens int) []byte {
	random := newFixtureRandom()
	tools := random.toolSet(toolSetSize)
	system := random.text(agenticSystemTokens*bytesPerToken, false)
	plan := random.sessionPlan(messages, calls)
	nominal := 0
	for _, step := range plan {
		nominal += step.nominal
	}
	budget := tokens*bytesPerToken - len(system) - len(mustMarshalJSON(t, tools))
	scale := float64(budget) / float64(nominal)
	list := make([]any, 0, len(plan)+1)
	list = append(list, plainMessage("system", system))
	callID := 0
	var pending []string
	for _, step := range plan {
		size := max(int(float64(step.nominal)*scale), minimumStepBytes)
		switch step.role {
		case "assistant":
			issued := make([]any, 0, step.calls)
			pending = pending[:0]
			for range step.calls {
				callID++
				id := "call_" + strconv.Itoa(callID)
				pending = append(pending, id)
				note := random.text(max(size-argumentNoteSlackBytes, argumentNoteMinBytes), false)
				arguments := fmt.Sprintf(`{"path":"src/pkg/file_%d.go","limit":%d,"note":%q}`, callID, callID%callIdentifierModulus, note)
				issued = append(issued, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "tool_" + strconv.Itoa(callID%toolSetSize), "arguments": arguments}})
			}
			list = append(list, map[string]any{"role": "assistant", "tool_calls": issued})
		case "tool":
			list = append(list, map[string]any{"role": "tool", "tool_call_id": pending[0], "content": random.text(size, true)})
			pending = pending[1:]
		default:
			list = append(list, plainMessage("user", random.text(size, false)))
		}
	}
	return marshalBody(t, map[string]any{"model": fixtureModel, "max_tokens": fixtureAgenticMaxTokens, "tools": tools, "messages": list})
}

func contentPartsBody(t testing.TB, parts, tokens int) []byte {
	random := newFixtureRandom()
	content := make([]any, 0, parts)
	for range parts {
		content = append(content, map[string]any{"type": "text", "text": random.text(tokens*bytesPerToken/parts, false)})
	}
	return marshalBody(t, map[string]any{"model": fixtureModel, "max_tokens": fixtureMaxTokens, "messages": []any{map[string]any{"role": "user", "content": content}}})
}

func imageContentBody(t testing.TB, images, size int) []byte {
	random := newFixtureRandom()
	content := make([]any, 0, images+1)
	content = append(content, map[string]any{"type": "text", "text": "describe these"})
	for range images {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + random.text(size, false)}})
	}
	return marshalBody(t, map[string]any{"model": fixtureModel, "max_tokens": fixtureMaxTokens, "messages": []any{map[string]any{"role": "user", "content": content}}})
}

func responseFormatBody(t testing.TB, patterns []string, tokens int) []byte {
	properties := map[string]any{}
	for index := range responseFormatFields {
		property := map[string]any{"type": "string", "description": "field " + strconv.Itoa(index)}
		if index < len(patterns) {
			property["pattern"] = patterns[index]
		}
		properties["field_"+strconv.Itoa(index)] = property
	}
	schema := map[string]any{"type": "object", "properties": properties, "required": []any{"field_0"}}
	return marshalBody(t, map[string]any{"model": fixtureModel, "max_tokens": fixtureMaxTokens,
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "extract", "strict": true, "schema": schema}},
		"messages":        []any{plainMessage("user", newFixtureRandom().text(tokens*bytesPerToken, false))}})
}

type measured struct {
	name  string
	used  int
	limit int
}

func (m measured) factor() float64 { return float64(m.limit) / float64(max(m.used, 1)) }

type shapeStats struct{ nodes, members, depth int }

func (stats *shapeStats) add(value any, depth int) {
	var children []any
	switch typed := value.(type) {
	case []any:
		children = typed
	case map[string]any:
		for _, child := range typed {
			children = append(children, child)
		}
	default:
		return
	}
	stats.depth = max(stats.depth, depth)
	stats.members = max(stats.members, len(children))
	stats.nodes += max(len(children), 1)
	for _, child := range children {
		stats.add(child, depth+1)
	}
}

func measureBounds(t testing.TB, body []byte) []measured {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	var stats shapeStats
	stats.add(decoded, 1)
	messages, _ := decoded["messages"].([]any)
	tools, _ := decoded["tools"].([]any)
	loose := SchemaBounds{MaxDepth: unboundedDepthLimit, MaxNodes: unboundedSchemaLimit, MaxSizeBytes: unboundedSchemaLimit, MaxBranch: unboundedDepthLimit, MaxEnum: unboundedDepthLimit, MaxPatternLen: toolsMaxPatternLen}
	budget := &patternBudget{limit: unboundedSchemaLimit}
	maxToolNodes, maxToolSize := 0, 0
	for _, item := range tools {
		parameters := item.(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
		nodes := 0
		if err := loose.walk(parameters, 1, &nodes, budget); err != nil {
			t.Fatalf("measure tool schema: %v", err)
		}
		size, err := jsonMarshaledSize(parameters)
		if err != nil {
			t.Fatalf("measure tool schema size: %v", err)
		}
		maxToolNodes, maxToolSize = max(maxToolNodes, nodes), max(maxToolSize, size)
	}
	if format, ok := decoded["response_format"].(map[string]any); ok {
		schema := format["json_schema"].(map[string]any)["schema"].(map[string]any)
		nodes := 0
		if err := loose.walk(schema, 1, &nodes, budget); err != nil {
			t.Fatalf("measure response_format schema: %v", err)
		}
	}
	return []measured{
		{"body_bytes", len(body), MaxBodyBytes},
		{"structural_nodes", stats.nodes, MaxStructuralNodes},
		{"container_members", stats.members, MaxContainerMembers},
		{"depth", stats.depth, MaxNestingDepth},
		{"messages", len(messages), messagesMaxEntries},
		{"tools", len(tools), defaultToolsMaxCount},
		{"tool_schema_nodes", maxToolNodes, toolsMaxNodes},
		{"tool_schema_bytes", maxToolSize, toolsMaxSizeBytes},
		{"pattern_bytes", budget.bytes, defaultPatternBudgetBytes},
		{"host_wire_bytes", transport.PromptWireBytes(len(body)) + hostCatchUpReserveBytes, transport.MaxHostRequestBytes},
	}
}

// Test flow:
//  1. Build each realistic large-context body at 4 bytes per token: one huge message, agentic sessions of 3,000 and 10,000 messages with 128 tools, content-part arrays, a response_format with patterns.
//  2. Measure every pre-admission bound the body would use on its own, and its headroom factor (limit / used).
//  3. Run the body through NormalizeRequest with the default profile and assert it is admitted.
//  4. Assert every bound keeps at least 2x headroom for that legitimate shape.
func TestLargeContextBodiesAreAdmittedWithHeadroom(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"a_huge_user_message_400k", hugeUserMessageBody(t, largeContextTokens)},
		{"a_huge_user_message_1M", hugeUserMessageBody(t, hugeContextTokens)},
		{"b_agentic_3000_messages_400k", agenticSessionBody(t, agenticMinMessages, 1, largeContextTokens)},
		{"b_agentic_10000_messages_400k", agenticSessionBody(t, agenticMaxMessages, 1, largeContextTokens)},
		{"b_agentic_10000_messages_3_parallel_calls_400k", agenticSessionBody(t, agenticMaxMessages, fixtureParallelCalls, largeContextTokens)},
		{"c_content_parts_3000_text_parts_400k", contentPartsBody(t, contentPartsFewer, largeContextTokens)},
		{"c_content_parts_4000_text_parts_400k", contentPartsBody(t, contentPartsMore, largeContextTokens)},
		{"d_response_format_24_patterns_400k", responseFormatBody(t, responseFormatPatternSet(responseFormatPatterns), largeContextTokens)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rows := measureBounds(t, testCase.body)
			for _, row := range rows {
				t.Logf("%-40s %-18s used=%-9d limit=%-9d headroom=%.2fx", testCase.name, row.name, row.used, row.limit, row.factor())
			}
			if _, err := NormalizeRequest(testCase.body, largeContextOptions); err != nil {
				t.Errorf("NormalizeRequest(%s) = %v, want admitted", testCase.name, err)
			}
			for _, row := range rows {
				if row.factor() < requiredHeadroom {
					t.Errorf("%s: bound %s used %d of %d, headroom %.2fx, want at least %.1fx", testCase.name, row.name, row.used, row.limit, row.factor(), requiredHeadroom)
				}
			}
		})
	}
}

// Test flow:
//  1. Build a body whose one user message carries a text part and four image_url parts.
//  2. Run it through NormalizeRequest.
//  3. Assert it is refused for the unsupported part type, not for any size bound.
func TestImageContentPartsAreRefusedByTextOnlyValidation(t *testing.T) {
	_, err := NormalizeRequest(imageContentBody(t, imageCount, imageBytes), largeContextOptions)
	if err == nil || !strings.Contains(err.Error(), `unsupported value "image_url"`) {
		t.Fatalf("NormalizeRequest(image_url parts) = %v, want a refusal naming the unsupported image_url part type", err)
	}
}

// Test flow:
//  1. Build one body with exactly `messagesMaxEntries` one-word messages and one body with a message more.
//  2. Assert the first is admitted.
//  3. Assert the second is refused naming the message limit.
func TestMessageCountLimitAdmitsExactlyTheLimit(t *testing.T) {
	if _, err := NormalizeRequest(tinyMessagesBody(messagesMaxEntries), largeContextOptions); err != nil {
		t.Errorf("NormalizeRequest(%d messages) = %v, want admitted", messagesMaxEntries, err)
	}
	_, err := NormalizeRequest(tinyMessagesBody(messagesMaxEntries+1), largeContextOptions)
	want := fmt.Sprintf("messages: array length %d exceeds limit %d", messagesMaxEntries+1, messagesMaxEntries)
	if err == nil || err.Error() != want {
		t.Errorf("NormalizeRequest(%d messages) = %v, want %q", messagesMaxEntries+1, err, want)
	}
}
