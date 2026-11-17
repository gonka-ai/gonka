package filters

import (
	"errors"
	"testing"
)

const (
	switchesToolsField          = `"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`
	switchesJSONSchemaField     = `"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}`
	switchesParallelToolsFields = switchesToolsField + `,"parallel_tool_calls":true`
)

// Test flow:
//  1. Bind Qwen to a profile that rejects tools.
//  2. Assert a request with tools is a 400 and one without passes.
func TestToolsRejectedForModel(t *testing.T) {
	knobs := FilterKnobs{Tools: toolsPolicyPointer(ToolsReject)}

	_, withTools := normalizeWithKnobs(t, knobs, switchesToolsField)
	_, withoutTools := normalizeWithKnobs(t, knobs, `"stream":false`)

	if ErrorStatus(withTools, 0) != 400 {
		t.Errorf("with tools: error = %v, want a 400", withTools)
	}
	if withoutTools != nil {
		t.Errorf("without tools: error = %v, want nil", withoutTools)
	}
}

// Test flow:
//  1. Bind Qwen to a profile that strips parallel_tool_calls.
//  2. Assert a request with tools keeps them but loses parallel_tool_calls.
func TestParallelToolCallsStripped(t *testing.T) {
	strip := ParallelToolCallsStrip
	document, err := normalizeWithKnobs(t, FilterKnobs{ParallelToolCalls: &strip}, switchesParallelToolsFields)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if document.Has("parallel_tool_calls") || !document.Has("tools") {
		t.Errorf("parallel_tool_calls kept = %v, tools kept = %v; want stripped and kept", document.Has("parallel_tool_calls"), document.Has("tools"))
	}
}

// Test flow:
//  1. Bind Qwen to a profile that rejects json_schema response formats.
//  2. Assert json_schema is a 400 and json_object passes.
func TestJSONSchemaRejectedForModel(t *testing.T) {
	knobs := FilterKnobs{RejectJSONSchema: boolPointer(true)}

	_, schema := normalizeWithKnobs(t, knobs, switchesJSONSchemaField)
	_, object := normalizeWithKnobs(t, knobs, `"response_format":{"type":"json_object"}`)

	if ErrorStatus(schema, 0) != 400 {
		t.Errorf("json_schema: error = %v, want a 400", schema)
	}
	if object != nil {
		t.Errorf("json_object: error = %v, want nil", object)
	}
}

// Test flow:
//  1. Normalize tools, parallel_tool_calls and a json_schema response format with no switches set.
//  2. Assert all three reach the host untouched.
func TestSwitchesOffLeaveRequestsUnchanged(t *testing.T) {
	document, err := normalizeWithKnobs(t, FilterKnobs{}, switchesParallelToolsFields+`,`+switchesJSONSchemaField)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	for _, field := range []string{"tools", "parallel_tool_calls", "response_format"} {
		if !document.Has(field) {
			t.Errorf("%s was dropped", field)
		}
	}
}

// Test flow:
//  1. Compose unknown values for the tools and parallel_tool_calls switches.
//  2. Assert both are refused as invalid settings.
func TestComposeRefusesUnknownSwitchValues(t *testing.T) {
	unknownParallel := ParallelToolCallsPolicy("sometimes")
	for name, knobs := range map[string]FilterKnobs{
		"tools":               {Tools: toolsPolicyPointer("maybe")},
		"parallel_tool_calls": {ParallelToolCalls: &unknownParallel},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, ModelSetting{Filters: knobs}); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

func toolsPolicyPointer(value ToolsPolicy) *ToolsPolicy { return &value }
