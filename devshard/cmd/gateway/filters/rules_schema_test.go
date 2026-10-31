package filters

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(raw)
}

func toolWithParams(t *testing.T, schema map[string]any) string {
	t.Helper()
	return `{"type":"function","function":{"name":"x","description":"x","parameters":` + mustMarshalJSON(t, schema) + `}}`
}

func testToolsBounds() SchemaBounds {
	return SchemaBounds{MaxDepth: toolsMaxDepth, MaxNodes: toolsMaxNodes, MaxSizeBytes: toolsMaxSizeBytes, MaxBranch: toolsMaxBranch, MaxEnum: toolsMaxEnum, MaxPatternLen: toolsMaxPatternLen}
}

func runRule(t *testing.T, document *Document, param string, rule RuleFunc) error {
	t.Helper()
	return rule(RuleContext{Document: document, Param: param})
}

// Test flow:
//  1. Run table cases of `validTools(bounds, "auto")` against bodies with no tools, an empty tools array, a parameter-less tool, a tool with simple parameters, and two valid tools.
//  2. Assert every case is accepted.
func TestValidToolsAccepts(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"messages":[]}`},
		{"empty array", `{"tools":[]}`},
		{"parameter-less tool with name", `{"tools":[{"type":"function","function":{"name":"x"}}]}`},
		{"simple parameters", `{"tools":[` + toolWithParams(t, map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}) + `]}`},
		{"two tools both valid", `{"tools":[` + toolWithParams(t, map[string]any{"type": "object"}) + `,` + toolWithParams(t, map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}}) + `]}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "tools", rule); err != nil {
				t.Fatalf("validTools() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `validTools(bounds, "auto")` against malformed tools: wrong wrapper shape, a non-object element, a missing or invalid type, a missing or invalid function, a missing or invalid function name, a forbidden `$ref` in parameters, and a bad schema in a second tool.
//  2. Assert each case's exact error message.
func TestValidToolsRejects(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"tools is object", `{"tools":{"x":1}}`, "tools: invalid array shape: must be an array"},
		{"tools element is not object", `{"tools":["x"]}`, "tools[i]: invalid tool shape: tools[0] must be an object"},
		{"type missing", `{"tools":[{"function":{"name":"x"}}]}`, "tools[i].type: must be \"function\" (tools[0])"},
		{"type not a string", `{"tools":[{"type":1,"function":{"name":"x"}}]}`, "tools[i].type: must be \"function\" (tools[0])"},
		{"type unknown value", `{"tools":[{"type":"plugin","function":{"name":"x"}}]}`, "tools[i].type: must be \"function\" (tools[0])"},
		{"function missing", `{"tools":[{"type":"function"}]}`, "tools[i].function: invalid wrapper shape: tools[0].function must be an object"},
		{"function not an object", `{"tools":[{"type":"function","function":"x"}]}`, "tools[i].function: invalid wrapper shape: tools[0].function must be an object"},
		{"function name missing", `{"tools":[{"type":"function","function":{}}]}`, "tools[i].function.name: must be a non-empty string (tools[0])"},
		{"function name empty", `{"tools":[{"type":"function","function":{"name":""}}]}`, "tools[i].function.name: must be a non-empty string (tools[0])"},
		{"function name whitespace", `{"tools":[{"type":"function","function":{"name":"   "}}]}`, "tools[i].function.name: must be a non-empty string (tools[0])"},
		{"function name not a string", `{"tools":[{"type":"function","function":{"name":42}}]}`, "tools[i].function.name: must be a non-empty string (tools[0])"},
		{"ref hidden in tool parameters", `{"tools":[` + toolWithParams(t, map[string]any{"$ref": "#/foo"}) + `]}`, `tools[0].function.parameters: schema reference keyword is forbidden: "$ref" is not allowed`},
		{"rejects bad schema in second tool", `{"tools":[` + toolWithParams(t, map[string]any{"type": "object"}) + `,` + toolWithParams(t, map[string]any{"$ref": "#/x"}) + `]}`, `tools[1].function.parameters: schema reference keyword is forbidden: "$ref" is not allowed`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := runRule(t, document, "tools", rule)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("validTools() = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run `validTools` with a shallow MaxDepth bound against a tool whose parameters nest one level past that depth.
//  2. Assert the rejection is prefixed with `tools[0].function.parameters`, proving the bounds check is wired with that field path.
func TestValidToolsAppliesBoundsWithFieldPathPrefix(t *testing.T) {
	shallow := SchemaBounds{MaxDepth: 3, MaxNodes: 100, MaxSizeBytes: 100000, MaxBranch: 100, MaxEnum: 100}
	rule := validTools(shallow, "auto")
	document := parseTestDocument(t, `{"tools":[`+toolWithParams(t, nestedPropertiesSchema(4))+`]}`)
	err := runRule(t, document, "tools", rule)
	want := "tools[0].function.parameters: nesting depth exceeded: limit 3"
	if err == nil || err.Error() != want {
		t.Fatalf("validTools() = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Run `validTools` against an empty tools array with `tool_choice:"auto"`.
//  2. Assert both `tools` and `tool_choice` are dropped.
func TestValidToolsStripsBothWhenToolsEmpty(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[],"tool_choice":"auto"}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if document.Has("tools") || document.Has("tool_choice") {
		t.Fatal("want both tools and tool_choice dropped")
	}
}

// Test flow:
//  1. Run `validTools` against an empty tools array with `tool_choice:"required"`.
//  2. Assert both `tools` and `tool_choice` are dropped, even though the tool_choice value is otherwise invalid.
func TestValidToolsStripsBothWhenToolsEmptyEvenWithBadToolChoice(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[],"tool_choice":"required"}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if document.Has("tools") || document.Has("tool_choice") {
		t.Fatal("want both tools and tool_choice dropped")
	}
}

// Test flow:
//  1. Run `validTools` against a valid tool with no `tool_choice`.
//  2. Assert `tool_choice` is defaulted to "auto".
func TestValidToolsDefaultsToolChoiceToAutoWhenAbsent(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}]}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if got, _ := document.Get("tool_choice"); got != "auto" {
		t.Fatalf("tool_choice = %v, want auto", got)
	}
}

// Test flow:
//  1. Run `validTools` against a valid tool with `tool_choice:"required"` and a `min_tokens`.
//  2. Assert `tool_choice` is coerced to "auto" and `min_tokens` is deleted.
func TestValidToolsCoercesRequiredToDefault(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"required","min_tokens":10}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if got, _ := document.Get("tool_choice"); got != "auto" {
		t.Fatalf("tool_choice = %v, want auto", got)
	}
	if document.Has("min_tokens") {
		t.Fatal("min_tokens survived a coerced required choice, want it deleted")
	}
}

// Test flow:
//  1. Run table cases of `validTools` against a `tool_choice` value (required, auto, a malformed number, a malformed unknown string, or a valid function object) with tools absent.
//  2. Assert `tool_choice` is deleted in every case, since vLLM's check_tool_usage would otherwise reject a non-"none" choice sent without tools.
func TestValidToolsDeletesToolChoiceWhenToolsAbsentRegardlessOfValue(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"required", `{"tool_choice":"required"}`},
		{"auto", `{"tool_choice":"auto"}`},
		{"malformed number", `{"tool_choice":42}`},
		{"malformed unknown string", `{"tool_choice":"force"}`},
		{"valid function object", `{"tool_choice":{"type":"function","function":{"name":"x"}}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "tools", rule); err != nil {
				t.Fatalf("validTools() = %v, want nil", err)
			}
			if document.Has("tool_choice") {
				t.Fatal("tool_choice must be deleted when tools is absent")
			}
		})
	}
}

// Test flow:
//  1. Run `validTools` against a valid tool with `tool_choice:"none"`.
//  2. Assert `tool_choice` stays "none".
func TestValidToolsDoesNotOverrideExplicitToolChoice(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"none"}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if got, _ := document.Get("tool_choice"); got != "none" {
		t.Fatalf("tool_choice = %v, want none", got)
	}
}

// Test flow:
//  1. Run `validTools` against a document with neither `tools` nor `tool_choice`.
//  2. Assert `tool_choice` stays absent.
func TestValidToolsLeavesToolChoiceAbsentWhenToolsAbsent(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	if document.Has("tool_choice") {
		t.Fatal("tool_choice must stay absent")
	}
}

// Test flow:
//  1. Run `validTools` against a tool whose function carries `strict:true` alongside `parameters`.
//  2. Assert `function.strict` is deleted while `name` and `parameters` survive.
func TestValidToolsStripsFunctionStrict(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x","strict":true,"parameters":{"type":"object"}}}]}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	tools, _ := document.Get("tools")
	function := tools.([]any)[0].(map[string]any)["function"].(map[string]any)
	if _, ok := function["strict"]; ok {
		t.Fatal("function.strict must be deleted")
	}
	if function["name"] != "x" {
		t.Fatalf("function.name = %v, want x", function["name"])
	}
	if _, ok := function["parameters"]; !ok {
		t.Fatal("function.parameters must survive")
	}
}

// Test flow:
//  1. Run `validTools` against two tools, each with a different `strict` value.
//  2. Assert `function.strict` is deleted from every tool.
func TestValidToolsStripsFunctionStrictAcrossMultipleTools(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"a","strict":true}},{"type":"function","function":{"name":"b","strict":false}}]}`)
	if err := runRule(t, document, "tools", rule); err != nil {
		t.Fatalf("validTools() = %v, want nil", err)
	}
	tools, _ := document.Get("tools")
	for _, item := range tools.([]any) {
		function := item.(map[string]any)["function"].(map[string]any)
		if _, ok := function["strict"]; ok {
			t.Fatal("function.strict must be deleted on every tool")
		}
	}
}

// Test flow:
//  1. Run table cases of `validToolChoice(toolChoiceMaxNameLen)` against an absent field, "auto", "none", and a function object.
//  2. Assert every case is accepted.
func TestValidToolChoiceAccepts(t *testing.T) {
	rule := validToolChoice(toolChoiceMaxNameLen)
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"messages":[]}`},
		{"auto", `{"tool_choice":"auto"}`},
		{"none", `{"tool_choice":"none"}`},
		{"function object", `{"tool_choice":{"type":"function","function":{"name":"web_search"}}}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "tool_choice", rule); err != nil {
				t.Fatalf("validToolChoice() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `validToolChoice(toolChoiceMaxNameLen)` against unknown strings, numbers, booleans, arrays, and malformed function objects (missing or wrong type, missing or invalid function, missing, blank, non-string, or over-length name).
//  2. Assert each case's exact error message.
func TestValidToolChoiceRejects(t *testing.T) {
	rule := validToolChoice(toolChoiceMaxNameLen)
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"unknown string", `{"tool_choice":"force"}`, "tool_choice: invalid value: must be \"auto\", \"none\", or a function object"},
		{"required reaches validator without upstream coerce", `{"tool_choice":"required"}`, "tool_choice: invalid value: must be \"auto\", \"none\", or a function object"},
		{"number", `{"tool_choice":42}`, "tool_choice: invalid value: must be \"auto\", \"none\", or a function object"},
		{"boolean", `{"tool_choice":true}`, "tool_choice: invalid value: must be \"auto\", \"none\", or a function object"},
		{"array", `{"tool_choice":["auto"]}`, "tool_choice: invalid value: must be \"auto\", \"none\", or a function object"},
		{"object missing type", `{"tool_choice":{"function":{"name":"x"}}}`, "tool_choice.function: invalid shape: type must be \"function\""},
		{"object wrong type", `{"tool_choice":{"type":"plugin","function":{"name":"x"}}}`, "tool_choice.function: invalid shape: type must be \"function\""},
		{"object missing function", `{"tool_choice":{"type":"function"}}`, "tool_choice.function: invalid shape: function must be an object"},
		{"object function not object", `{"tool_choice":{"type":"function","function":"x"}}`, "tool_choice.function: invalid shape: function must be an object"},
		{"object missing name", `{"tool_choice":{"type":"function","function":{}}}`, "tool_choice.function: invalid shape: function.name must be a non-empty string"},
		{"object empty name", `{"tool_choice":{"type":"function","function":{"name":""}}}`, "tool_choice.function: invalid shape: function.name must be a non-empty string"},
		{"object whitespace name", `{"tool_choice":{"type":"function","function":{"name":"   "}}}`, "tool_choice.function: invalid shape: function.name must be a non-empty string"},
		{"object non-string name", `{"tool_choice":{"type":"function","function":{"name":42}}}`, "tool_choice.function: invalid shape: function.name must be a non-empty string"},
		{"name exceeds length cap", `{"tool_choice":{"type":"function","function":{"name":"` + strings.Repeat("x", 65) + `"}}}`, "tool_choice.function: invalid shape: function.name length 65 exceeds limit 64"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := runRule(t, document, "tool_choice", rule)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("validToolChoice() = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Build a tool's `parameters` schema padded to serialize to exactly `toolsMaxSizeBytes`, and check it.
//  2. Build the same schema padded one byte past that cap, and check it.
//  3. Assert the exact-size case is accepted and the one-byte-over case is rejected with the serialized-size error.
func TestValidToolsParametersByteCapBoundary(t *testing.T) {
	rule := validTools(testToolsBounds(), "auto")
	buildParameters := func(t *testing.T, size int) map[string]any {
		t.Helper()
		schema := map[string]any{"type": "object", "description": ""}
		baseSize, err := jsonMarshaledSize(schema)
		if err != nil {
			t.Fatalf("jsonMarshaledSize: %v", err)
		}
		schema["description"] = strings.Repeat("a", size-baseSize)
		return schema
	}
	t.Run("exactly at the cap is accepted", func(t *testing.T) {
		document := parseTestDocument(t, `{"tools":[`+toolWithParams(t, buildParameters(t, toolsMaxSizeBytes))+`]}`)
		if err := runRule(t, document, "tools", rule); err != nil {
			t.Fatalf("validTools() = %v, want nil", err)
		}
	})
	t.Run("one byte over the cap is rejected", func(t *testing.T) {
		document := parseTestDocument(t, `{"tools":[`+toolWithParams(t, buildParameters(t, toolsMaxSizeBytes+1))+`]}`)
		err := runRule(t, document, "tools", rule)
		want := "tools[0].function.parameters: serialized size exceeded: limit 65536 bytes"
		if err == nil || err.Error() != want {
			t.Fatalf("validTools() = %v, want %q", err, want)
		}
	})
}

// Test flow:
//  1. Run table cases of `parallelToolCalls()` against a valid true, a valid false, and a malformed `parallel_tool_calls` value, all with `tools` absent.
//  2. Assert `parallel_tool_calls` is deleted in every case (the empty-tools-array case is covered separately by `TestNormalizeRequestDropsToolControlsWithoutTools`, since only the pipeline runs `validTools` before this rule).
func TestParallelToolCallsDropsWhenToolsAbsentRegardlessOfValue(t *testing.T) {
	rule := parallelToolCalls()
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"valid true, tools absent", `{"parallel_tool_calls":true}`},
		{"valid false, tools absent", `{"parallel_tool_calls":false}`},
		{"malformed value, tools absent", `{"parallel_tool_calls":"yes"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "parallel_tool_calls", rule); err != nil {
				t.Fatalf("parallelToolCalls() = %v, want nil", err)
			}
			if document.Has("parallel_tool_calls") {
				t.Fatal("parallel_tool_calls must be deleted when tools is absent")
			}
		})
	}
}

// Test flow:
//  1. Run `parallelToolCalls()` against a document with `tools` present and a malformed `parallel_tool_calls` value.
//  2. Assert it is rejected with the must-be-a-boolean error.
func TestParallelToolCallsValidatesWhenToolsPresent(t *testing.T) {
	rule := parallelToolCalls()
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"parallel_tool_calls":"yes"}`)
	err := runRule(t, document, "parallel_tool_calls", rule)
	want := "parallel_tool_calls: must be a boolean"
	if err == nil || err.Error() != want {
		t.Fatalf("parallelToolCalls() = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Run `parallelToolCalls()` against a document with `tools` present and `parallel_tool_calls:true`.
//  2. Assert the value passes through unchanged.
func TestParallelToolCallsPassesThroughAValidBoolWhenToolsPresent(t *testing.T) {
	rule := parallelToolCalls()
	document := parseTestDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"parallel_tool_calls":true}`)
	if err := runRule(t, document, "parallel_tool_calls", rule); err != nil {
		t.Fatalf("parallelToolCalls() = %v, want nil", err)
	}
	if got, _ := document.Get("parallel_tool_calls"); got != true {
		t.Fatalf("parallel_tool_calls = %v, want true", got)
	}
}

// normalizeToolsTestRequest is the shared shell every NormalizeRequest-level tools/tool_choice case wraps.
func normalizeToolsTestRequest(t *testing.T, body string) (*Document, error) {
	t.Helper()
	result, err := NormalizeRequest([]byte(body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 4096})
	if err != nil {
		return nil, err
	}
	document, parseErr := ParseDocument(result.Body)
	if parseErr != nil {
		t.Fatalf("ParseDocument(%s) = %v", result.Body, parseErr)
	}
	return document, nil
}

// Test flow:
//  1. Call NormalizeRequest through the `normalizeToolsTestRequest` helper for bodies with a required or malformed `tool_choice` and `parallel_tool_calls` but no tools, and for an empty tools array with both controls set.
//  2. Assert every case is accepted rather than rejected.
//  3. Assert `tools`, `tool_choice`, and `parallel_tool_calls` are all dropped from the normalized body.
func TestNormalizeRequestDropsToolControlsWithoutTools(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"required tool_choice and true parallel_tool_calls, no tools",
			`{"messages":[{"role":"user","content":"hi"}],"tool_choice":"required","parallel_tool_calls":true}`},
		{"malformed tool_choice and malformed parallel_tool_calls, no tools",
			`{"messages":[{"role":"user","content":"hi"}],"tool_choice":42,"parallel_tool_calls":"yes"}`},
		{"empty tools array with tool_choice and parallel_tool_calls",
			`{"messages":[{"role":"user","content":"hi"}],"tools":[],"tool_choice":"auto","parallel_tool_calls":false}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document, err := normalizeToolsTestRequest(t, testCase.body)
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance: a request without tools must have its tool controls dropped, not rejected", err)
			}
			for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
				if document.Has(field) {
					t.Errorf("%s must be dropped", field)
				}
			}
		})
	}
}

// Test flow:
//  1. Call NormalizeRequest with a valid tool and `tool_choice:"required"`.
//  2. Assert the normalized body's `tool_choice` is coerced to "auto".
func TestNormalizeRequestCoercesRequiredToolChoiceOnlyWhenToolsPresent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"required"}`
	document, err := normalizeToolsTestRequest(t, body)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	if got, _ := document.Get("tool_choice"); got != "auto" {
		t.Fatalf("tool_choice = %v, want auto", got)
	}
}

// Test flow:
//  1. Call NormalizeRequest with a valid tool and a malformed `parallel_tool_calls` value.
//  2. Assert normalization rejects the request with the must-be-a-boolean error.
func TestNormalizeRequestRejectsMalformedParallelToolCallsWhenToolsPresent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x"}}],"parallel_tool_calls":"yes"}`
	_, err := normalizeToolsTestRequest(t, body)
	want := "parallel_tool_calls: must be a boolean"
	if err == nil || err.Error() != want {
		t.Fatalf("NormalizeRequest() = %v, want rejection %q", err, want)
	}
}

func testResponseFormatBounds() SchemaBounds {
	return SchemaBounds{MaxDepth: responseFormatMaxDepth, MaxNodes: responseFormatMaxNodes, MaxSizeBytes: responseFormatMaxSizeBytes, MaxBranch: responseFormatMaxBranch, MaxEnum: responseFormatMaxEnum, MaxPatternLen: responseFormatMaxPatternLen}
}

func jsonSchemaResponseFormatBody(t *testing.T, schema map[string]any) string {
	t.Helper()
	return `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + mustMarshalJSON(t, schema) + `}}}`
}

// Test flow:
//  1. Run table cases of `validResponseFormat(bounds, maxNameLen)` against an absent field, `type:"text"`, `type:"json_object"`, a simple `json_schema`, a name with dots/dashes/underscores, and a schema type given as an array of primitives.
//  2. Assert every case is accepted.
func TestValidResponseFormatAccepts(t *testing.T) {
	rule := validResponseFormat(testResponseFormatBounds(), responseFormatMaxNameLen)
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"messages":[]}`},
		{"type text", `{"response_format":{"type":"text"}}`},
		{"type json_object", `{"response_format":{"type":"json_object"}}`},
		{"json_schema simple", `{"response_format":{"type":"json_schema","json_schema":{"name":"weather_v1","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}}`},
		{"json_schema name with dots dashes underscores", `{"response_format":{"type":"json_schema","json_schema":{"name":"abc_DEF-1.2","schema":{"type":"object"}}}}`},
		{"schema type as array of primitives", jsonSchemaResponseFormatBody(t, map[string]any{"type": []any{"string", "null"}})},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "response_format", rule); err != nil {
				t.Fatalf("validResponseFormat() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `validResponseFormat(bounds, maxNameLen)` against a non-object wrapper, a missing/empty/unknown type, a missing/invalid `json_schema` wrapper, a missing/blank/invalid/over-length schema name, a missing/non-object schema, and a forbidden `$ref` inside the schema.
//  2. Assert each case's exact error message.
func TestValidResponseFormatRejects(t *testing.T) {
	rule := validResponseFormat(testResponseFormatBounds(), responseFormatMaxNameLen)
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"response_format not an object", `{"response_format":"hi"}`, "response_format: invalid wrapper shape: must be an object"},
		{"type missing", `{"response_format":{"json_schema":{"name":"r","schema":{"type":"object"}}}}`, "response_format.type: missing or unsupported: must be a non-empty string"},
		{"type empty string", `{"response_format":{"type":""}}`, "response_format.type: missing or unsupported: must be a non-empty string"},
		{"unknown type", `{"response_format":{"type":"banana"}}`, `response_format.type: missing or unsupported: "banana" is not supported (allowed: text, json_object, json_schema)`},
		{"json_schema wrapper missing", `{"response_format":{"type":"json_schema"}}`, "response_format.json_schema: invalid wrapper shape: must be an object"},
		{"json_schema missing name", `{"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`, "response_format.json_schema.name: invalid: must be a non-empty string"},
		{"json_schema whitespace name", `{"response_format":{"type":"json_schema","json_schema":{"name":"   ","schema":{"type":"object"}}}}`, "response_format.json_schema.name: invalid: must be a non-empty string"},
		{"json_schema name has bad chars", `{"response_format":{"type":"json_schema","json_schema":{"name":"bad name","schema":{"type":"object"}}}}`, "response_format.json_schema.name: invalid: must match ^[A-Za-z0-9_.-]+$"},
		{"json_schema name too long", `{"response_format":{"type":"json_schema","json_schema":{"name":"` + strings.Repeat("a", 65) + `","schema":{"type":"object"}}}}`, "response_format.json_schema.name: invalid: must be 64 characters or fewer"},
		{"json_schema missing schema", `{"response_format":{"type":"json_schema","json_schema":{"name":"r"}}}`, "response_format.json_schema.schema: invalid shape: must be an object"},
		{"schema not an object", `{"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":"x"}}}`, "response_format.json_schema.schema: invalid shape: must be an object"},
		{"ref not allowed", jsonSchemaResponseFormatBody(t, map[string]any{"$ref": "#/foo"}), `response_format.json_schema.schema: schema reference keyword is forbidden: "$ref" is not allowed`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := runRule(t, document, "response_format", rule)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("validResponseFormat() = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run `validResponseFormat` with a shallow MaxDepth bound against a `json_schema` whose schema nests one level past that depth.
//  2. Assert the rejection is prefixed with `response_format.json_schema.schema`, proving the bounds check is wired with that field path.
func TestValidResponseFormatAppliesBoundsWithFieldPathPrefix(t *testing.T) {
	shallow := SchemaBounds{MaxDepth: 3, MaxNodes: 100, MaxSizeBytes: 100000, MaxBranch: 100, MaxEnum: 100}
	rule := validResponseFormat(shallow, responseFormatMaxNameLen)
	document := parseTestDocument(t, jsonSchemaResponseFormatBody(t, nestedPropertiesSchema(4)))
	err := runRule(t, document, "response_format", rule)
	want := "response_format.json_schema.schema: nesting depth exceeded: limit 3"
	if err == nil || err.Error() != want {
		t.Fatalf("validResponseFormat() = %v, want %q", err, want)
	}
}

func testStructuredOutputsBounds() structuredOutputsBounds {
	return structuredOutputsBounds{
		SchemaBounds:        SchemaBounds{MaxDepth: structuredOutputsMaxDepth, MaxNodes: structuredOutputsMaxNodes, MaxSizeBytes: structuredOutputsMaxSizeBytes, MaxBranch: structuredOutputsMaxBranch, MaxEnum: structuredOutputsMaxEnum, MaxPatternLen: structuredOutputsMaxPatternLen},
		MaxChoiceEntries:    structuredOutputsMaxChoiceEntries,
		MaxChoiceEntryLen:   structuredOutputsMaxChoiceEntryLen,
		MaxGrammarLen:       structuredOutputsMaxGrammarLen,
		MaxGrammarNesting:   structuredOutputsMaxGrammarNesting,
		MaxStructuralTagLen: structuredOutputsMaxStructuralTagLen,
	}
}

// Test flow:
//  1. Look up `structuredOutputsConstraintValidators(bounds)` for every field in `structuredOutputsConstraintFields`.
//  2. Assert every field has a validator, keeping the two lists from drifting apart.
func TestStructuredOutputsConstraintValidatorsCoverAllFields(t *testing.T) {
	validators := structuredOutputsConstraintValidators(testStructuredOutputsBounds())
	for _, field := range structuredOutputsConstraintFields {
		if validators[field] == nil {
			t.Errorf("structured_outputs constraint %q has no validator", field)
		}
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a document with no `structured_outputs` field.
//  2. Assert it returns no error.
func TestValidStructuredOutputsAbsent(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := runRule(t, document, "structured_outputs", rule); err != nil {
		t.Fatalf("validStructuredOutputs() = %v, want nil", err)
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against `structured_outputs` values that are a string, a number, and an array.
//  2. Assert every case is rejected with the invalid-wrapper-shape error.
func TestValidStructuredOutputsRejectsNonObjectWrapper(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	for _, body := range []string{`{"structured_outputs":"x"}`, `{"structured_outputs":42}`, `{"structured_outputs":[]}`} {
		t.Run(body, func(t *testing.T) {
			document := parseTestDocument(t, body)
			err := runRule(t, document, "structured_outputs", rule)
			want := "structured_outputs: invalid wrapper shape: must be an object"
			if err == nil || err.Error() != want {
				t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
			}
		})
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a document carrying both `response_format` and `structured_outputs`.
//  2. Assert it is rejected as an unsupported combination.
func TestValidStructuredOutputsRejectsResponseFormatConflict(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"response_format":{"type":"json_object"},"structured_outputs":{"json_object":true}}`)
	err := runRule(t, document, "structured_outputs", rule)
	want := "structured_outputs: cannot be combined with response_format"
	if err == nil || err.Error() != want {
		t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` under the kimi profile against a `structured_outputs` request.
//  2. Assert the rejection's exact message text, which is golden-pinned by `schema_structured_outputs_rejected_for_kimi` and `profile_kimi_structured_outputs_rejected`.
func TestValidStructuredOutputsRejectsForProfileHook(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"structured_outputs":{"json_object":true}}`)
	err := rule(RuleContext{Document: document, Param: "structured_outputs", Profile: kimiProfile})
	want := "structured_outputs: not supported on this model — use response_format instead"
	if err == nil || err.Error() != want {
		t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
	}
}

// Test flow:
//  1. For the default (nil) and minimax profiles, run `validStructuredOutputs(bounds)` against a `structured_outputs` request.
//  2. Assert it is accepted for every profile without the hook.
func TestValidStructuredOutputsAcceptsWithoutProfileHook(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	for _, profile := range []*Profile{nil, minimaxProfile} {
		document := parseTestDocument(t, `{"structured_outputs":{"json_object":true}}`)
		if err := rule(RuleContext{Document: document, Param: "structured_outputs", Profile: profile}); err != nil {
			t.Errorf("validStructuredOutputs() = %v, want nil for profile %v", err, profile)
		}
	}
}

// Test flow:
//  1. Run table cases of `validStructuredOutputs(bounds)` against an empty envelope, an envelope with only an auxiliary field set, and an envelope with two constraints set.
//  2. Assert each case's exact error message reporting the wrong constraint count.
func TestValidStructuredOutputsEnforcesExactlyOne(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"empty envelope", `{"structured_outputs":{}}`, "structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set (got 0)"},
		{"zero constraints", `{"structured_outputs":{"disable_any_whitespace":true}}`, "structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set (got 0)"},
		{"two constraints", `{"structured_outputs":{"json":{"type":"string"},"regex":"\\d+"}}`, "structured_outputs: exactly one of json/regex/choice/grammar/json_object/structural_tag must be set (got 2: json, regex)"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := runRule(t, document, "structured_outputs", rule)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("validStructuredOutputs() = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against an envelope with one explicit `null` constraint alongside one set constraint.
//  2. Assert it is accepted, since a null constraint counts as absent.
//  3. Run it again against an envelope where all six constraints are explicit `null`.
//  4. Assert the rejection reports zero constraints set.
func TestValidStructuredOutputsTreatsExplicitNullAsAbsent(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("null plus one set counts as one", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":null,"regex":"\\d+"}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("all six null counts as zero", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":null,"regex":null,"choice":null,"grammar":null,"json_object":null,"structural_tag":null}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.Contains(err.Error(), "got 0") {
			t.Fatalf("validStructuredOutputs() = %v, want it to contain %q", err, "got 0")
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a `structured_outputs` object carrying `_backend` and `_backend_was_auto` alongside `json_object`.
//  2. Assert both private fields are stripped while `json_object` survives.
func TestValidStructuredOutputsStripsPrivateFields(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"structured_outputs":{"json_object":true,"_backend":"xgrammar","_backend_was_auto":true}}`)
	if err := runRule(t, document, "structured_outputs", rule); err != nil {
		t.Fatalf("validStructuredOutputs() = %v, want nil", err)
	}
	structuredOutputs, _ := document.Object("structured_outputs")
	if _, ok := structuredOutputs["_backend"]; ok {
		t.Fatal("_backend must be stripped")
	}
	if _, ok := structuredOutputs["_backend_was_auto"]; ok {
		t.Fatal("_backend_was_auto must be stripped")
	}
	if structuredOutputs["json_object"] != true {
		t.Fatal("json_object must survive the strip")
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a `structured_outputs` object carrying an unrecognized `backend` key.
//  2. Assert it is rejected as an unknown sub-field.
func TestValidStructuredOutputsRejectsUnknownSubField(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"structured_outputs":{"json_object":true,"backend":"outlines"}}`)
	err := runRule(t, document, "structured_outputs", rule)
	want := `structured_outputs: invalid wrapper shape: unknown sub-field "backend"`
	if err == nil || err.Error() != want {
		t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a bounded `json` schema, a string-encoded schema, and a schema whose nested value carries a `$ref`.
//  2. Assert the bounded schema is accepted, the string-encoded schema is rejected, and the `$ref` case is rejected with the `structured_outputs.json:` prefix.
func TestValidStructuredOutputsJSON(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts a bounded schema", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":{"type":"object","properties":{"x":{"type":"string"}}}}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects a string-encoded schema", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":"{\"type\":\"object\"}"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.json: must be an object (string-encoded schemas are not accepted)"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("rejects a $ref with the field-path prefix", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":{"type":"object","properties":{"x":{"$ref":"#/definitions/x"}}}}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.HasPrefix(err.Error(), "structured_outputs.json: ") {
			t.Fatalf("validStructuredOutputs() = %v, want it prefixed with %q", err, "structured_outputs.json: ")
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a normal pattern, a non-string value, an over-length pattern, and an uncompilable pattern.
//  2. Assert the normal pattern is accepted and each invalid case fails with its matching error.
func TestValidStructuredOutputsRegex(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts a normal pattern", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"regex":"^[a-z]+$"}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects non-string", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"regex":42}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.regex: invalid shape: must be a string"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("rejects over-length", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"regex":"`+strings.Repeat("a", 513)+`"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.regex: length exceeded: 513 > 512"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("rejects uncompilable", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"regex":"["}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.HasPrefix(err.Error(), "structured_outputs.regex: must compile as a regex: ") {
			t.Fatalf("validStructuredOutputs() = %v, want prefix %q", err, "structured_outputs.regex: must compile as a regex: ")
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a bounded list, an empty array, a non-string item, a list exactly at the entry-count limit, a list one entry over the limit, a single entry at the exact per-entry byte limit, entries summed exactly to the shared size cap, and entries that exceed the shared size cap.
//  2. Assert each case's pass/fail outcome and, where relevant, its exact or prefixed error message.
func TestValidStructuredOutputsChoice(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts a bounded list", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"choice":["yes","no","maybe"]}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects empty array", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[]}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.choice: must be a non-empty string array"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("rejects non-string item", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"choice":["a",42]}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.choice: must be a non-empty string array: choice[1] must be a string"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("accepts exactly the entry-count limit", func(t *testing.T) {
		entries := make([]string, structuredOutputsMaxChoiceEntries)
		for i := range entries {
			entries[i] = `"x"`
		}
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[`+strings.Join(entries, ",")+`]}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects too many entries", func(t *testing.T) {
		entries := make([]string, 257)
		for i := range entries {
			entries[i] = `"x"`
		}
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[`+strings.Join(entries, ",")+`]}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.choice: exceeded size limits: 257 entries > 256"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("accepts a single entry at the exact per-entry byte limit", func(t *testing.T) {
		entry := `"` + strings.Repeat("a", structuredOutputsMaxChoiceEntryLen) + `"`
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[`+entry+`]}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("accepts total length exactly at the shared size cap", func(t *testing.T) {
		var entries []string
		remaining := structuredOutputsMaxSizeBytes
		for remaining > 0 {
			entryLen := min(remaining, structuredOutputsMaxChoiceEntryLen)
			entries = append(entries, `"`+strings.Repeat("a", entryLen)+`"`)
			remaining -= entryLen
		}
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[`+strings.Join(entries, ",")+`]}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects when total length exceeds the shared size cap", func(t *testing.T) {
		entry := `"` + strings.Repeat("a", 1024) + `"`
		entries := make([]string, 20)
		for i := range entries {
			entries[i] = entry
		}
		document := parseTestDocument(t, `{"structured_outputs":{"choice":[`+strings.Join(entries, ",")+`]}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.HasPrefix(err.Error(), "structured_outputs.choice: exceeded size limits: total length ") {
			t.Fatalf("validStructuredOutputs() = %v, want prefix about total length", err)
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a simple grammar, a grammar nested exactly at the depth limit, a grammar one level past the limit (the CVE-2026-25048 proof-of-concept pattern), and a grammar whose balanced brackets do not accumulate depth.
//  2. Assert the accepted cases pass and the over-limit case fails with the nesting-depth error.
func TestValidStructuredOutputsGrammar(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts a simple grammar", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"grammar":"start: \"hello\""}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("accepts nesting depth exactly at the limit", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"grammar":"`+strings.Repeat("(", structuredOutputsMaxGrammarNesting)+`"}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects deep nesting (CVE-2026-25048 PoC pattern)", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"grammar":"`+strings.Repeat("(", 201)+`"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.grammar: nesting depth exceeded: 201 > 200"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("balanced brackets do not accumulate depth", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"grammar":"`+strings.Repeat("()", 250)+`"}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against `json_object` set to true and to false, and against a non-boolean value.
//  2. Assert the boolean cases are accepted and the non-boolean case fails with the must-be-a-boolean error.
func TestValidStructuredOutputsJSONObject(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts true and false", func(t *testing.T) {
		for _, body := range []string{`{"structured_outputs":{"json_object":true}}`, `{"structured_outputs":{"json_object":false}}`} {
			document := parseTestDocument(t, body)
			if err := runRule(t, document, "structured_outputs", rule); err != nil {
				t.Fatalf("validStructuredOutputs() = %v, want nil", err)
			}
		}
	})
	t.Run("rejects non-bool", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json_object":"true"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.json_object: must be a boolean"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against the object form of `structural_tag`, its rejected string form, an object padded to exactly the byte limit, and an oversize object.
//  2. Assert each case's pass/fail outcome and, where relevant, its exact or prefixed error message.
func TestValidStructuredOutputsStructuralTag(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts the object form", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"structural_tag":{"type":"structural_tag","structures":[],"triggers":[]}}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects the engine-crashing string form", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"structural_tag":"<tool>...</tool>"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs.structural_tag: invalid shape: must be an object (a JSON-encoded string crashes the engine)"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
	t.Run("accepts a structural_tag object at exactly the byte limit", func(t *testing.T) {
		template := map[string]any{"type": "structural_tag", "blob": ""}
		baseSize, err := jsonMarshaledSize(template)
		if err != nil {
			t.Fatalf("jsonMarshaledSize: %v", err)
		}
		template["blob"] = strings.Repeat("a", structuredOutputsMaxStructuralTagLen-baseSize)
		document := parseTestDocument(t, `{"structured_outputs":{"structural_tag":`+mustMarshalJSON(t, template)+`}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects an oversize object", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"structural_tag":{"type":"structural_tag","blob":"`+strings.Repeat("a", 4*1024+1)+`"}}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.HasPrefix(err.Error(), "structured_outputs.structural_tag: length exceeded: ") {
			t.Fatalf("validStructuredOutputs() = %v, want prefix about length exceeded", err)
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a `whitespace_pattern` alongside `json`, and against an uncompilable pattern.
//  2. Assert the valid pattern is accepted and the uncompilable one fails with the compile error.
func TestValidStructuredOutputsWhitespacePattern(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts a pattern alongside json", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":"\\s*"}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects uncompilable", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":"["}}`)
		err := runRule(t, document, "structured_outputs", rule)
		if err == nil || !strings.HasPrefix(err.Error(), "structured_outputs.whitespace_pattern: must compile as a regex: ") {
			t.Fatalf("validStructuredOutputs() = %v, want prefix about compiling as a regex", err)
		}
	})
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against both flags set to booleans, and against a non-boolean flag value.
//  2. Assert the boolean case is accepted and the non-boolean case fails, naming the offending flag.
func TestValidStructuredOutputsBoolFlags(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	t.Run("accepts bools on both flags", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json_object":true,"disable_any_whitespace":true,"disable_additional_properties":false}}`)
		if err := runRule(t, document, "structured_outputs", rule); err != nil {
			t.Fatalf("validStructuredOutputs() = %v, want nil", err)
		}
	})
	t.Run("rejects a non-bool flag", func(t *testing.T) {
		document := parseTestDocument(t, `{"structured_outputs":{"json_object":true,"disable_any_whitespace":"yes"}}`)
		err := runRule(t, document, "structured_outputs", rule)
		want := "structured_outputs: flag must be a boolean: disable_any_whitespace"
		if err == nil || err.Error() != want {
			t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
		}
	})
}

func testChatTemplateKwargsBounds() ObjectBounds {
	return ObjectBounds{MaxDepth: chatTemplateKwargsMaxDepth, MaxNodes: chatTemplateKwargsMaxNodes, MaxSizeBytes: chatTemplateKwargsMaxSizeBytes}
}

// Test flow:
//  1. Run table cases of `validChatTemplateKwargs(bounds)` against an absent field, an empty object, a kimi thinking shape, a qwen enable_thinking/preserve_thinking pair, an `add_generation_prompt` flag, and an array-valued entry.
//  2. Assert every case is accepted.
func TestValidChatTemplateKwargsAccepts(t *testing.T) {
	rule := validChatTemplateKwargs(testChatTemplateKwargsBounds())
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"messages":[]}`},
		{"empty object", `{"chat_template_kwargs":{}}`},
		{"kimi thinking shape", `{"chat_template_kwargs":{"thinking":true}}`},
		{"qwen enable_thinking plus preserve", `{"chat_template_kwargs":{"enable_thinking":true,"preserve_thinking":false}}`},
		{"add_generation_prompt is allowed", `{"chat_template_kwargs":{"add_generation_prompt":true}}`},
		{"array of strings", `{"chat_template_kwargs":{"tags":["a","b","c"]}}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := runRule(t, document, "chat_template_kwargs", rule); err != nil {
				t.Fatalf("validChatTemplateKwargs() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `validChatTemplateKwargs(bounds)` against a non-object wrapper, an array wrapper, each forbidden key that overrides an `apply_hf_chat_template` positional argument, a forbidden key alongside a legitimate one, and an object nested one level past the depth limit.
//  2. Assert each case's exact error message.
func TestValidChatTemplateKwargsRejects(t *testing.T) {
	rule := validChatTemplateKwargs(testChatTemplateKwargsBounds())
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"wrapper not object", `{"chat_template_kwargs":"hi"}`, "chat_template_kwargs: invalid wrapper shape: must be an object"},
		{"wrapper is array", `{"chat_template_kwargs":[1,2]}`, "chat_template_kwargs: invalid wrapper shape: must be an object"},
		{"forbidden chat_template key", `{"chat_template_kwargs":{"chat_template":"{% for x in range(99999) %}{% endfor %}"}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "chat_template"`},
		{"forbidden tokenize key", `{"chat_template_kwargs":{"tokenize":true}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "tokenize"`},
		{"forbidden tools key", `{"chat_template_kwargs":{"tools":[]}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "tools"`},
		{"forbidden documents key", `{"chat_template_kwargs":{"documents":[]}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "documents"`},
		{"forbidden conversation key", `{"chat_template_kwargs":{"conversation":[]}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "conversation"`},
		{"forbidden continue_final_message", `{"chat_template_kwargs":{"continue_final_message":true}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "continue_final_message"`},
		{"forbidden max_length", `{"chat_template_kwargs":{"max_length":1024}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "max_length"`},
		{"forbidden alongside legit", `{"chat_template_kwargs":{"thinking":true,"chat_template":"bomb"}}`, `chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): "chat_template"`},
		{"depth exceeds limit", `{"chat_template_kwargs":` + mustMarshalJSON(t, nestedObjectMap(chatTemplateKwargsMaxDepth+1)) + `}`, "chat_template_kwargs: nesting depth exceeded: limit 16"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := runRule(t, document, "chat_template_kwargs", rule)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("validChatTemplateKwargs() = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

// Test flow:
//  1. Assert every field in `structuredOutputsAuxiliaryFields` has a hand-written value in the local `valueByField` table, keeping the table honest as the field list grows (each field needs a value of its own type, so the table is written out rather than derived).
//  2. Run `validStructuredOutputs(bounds)` once per auxiliary field, alongside a `regex` constraint.
//  3. Assert every auxiliary field is accepted, since the known-field set is the only thing keeping it from being rejected as an unknown sub-field.
func TestStructuredOutputsAcceptsEveryAuxiliaryFieldBesideAConstraint(t *testing.T) {
	valueByField := map[string]string{
		"whitespace_pattern":            `" "`,
		"disable_any_whitespace":        `true`,
		"disable_additional_properties": `true`,
	}
	for _, field := range structuredOutputsAuxiliaryFields {
		if _, covered := valueByField[field]; !covered {
			t.Fatalf("auxiliary field %q has no value in this test, so nothing checks that it is accepted", field)
		}
	}

	rule := validStructuredOutputs(testStructuredOutputsBounds())
	for _, field := range structuredOutputsAuxiliaryFields {
		t.Run(field, func(t *testing.T) {
			body := `{"structured_outputs":{"regex":"a+","` + field + `":` + valueByField[field] + `}}`
			document := parseTestDocument(t, body)

			if err := runRule(t, document, "structured_outputs", rule); err != nil {
				t.Fatalf("validStructuredOutputs(%s) = %v, want the auxiliary field accepted", body, err)
			}
		})
	}
}

// Test flow:
//  1. Run `validStructuredOutputs(bounds)` against a `structured_outputs` object carrying an invented field alongside a `regex` constraint.
//  2. Assert it is rejected as an unknown sub-field.
func TestStructuredOutputsRejectsAFieldThatIsNeitherConstraintNorAuxiliary(t *testing.T) {
	rule := validStructuredOutputs(testStructuredOutputsBounds())
	document := parseTestDocument(t, `{"structured_outputs":{"regex":"a+","invented":true}}`)

	err := runRule(t, document, "structured_outputs", rule)

	want := `structured_outputs: invalid wrapper shape: unknown sub-field "invented"`
	if err == nil || err.Error() != want {
		t.Fatalf("validStructuredOutputs() = %v, want %q", err, want)
	}
}
