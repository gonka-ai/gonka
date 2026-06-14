package paramvalidators

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultToolsValidator() ToolsValidator {
	return ToolsValidator{
		MaxDepth:          16,
		MaxSize:           16 * 1024,
		MaxNodes:          256,
		MaxBranch:         16,
		MaxEnum:           256,
		MaxPatternLen:     512,
		DefaultToolChoice: "auto",
	}
}

func toolWithParams(schema string) string {
	return `{"type":"function","function":{"name":"x","description":"x","parameters":` + schema + `}}`
}

func TestToolsValidatorAccepts(t *testing.T) {
	v := defaultToolsValidator()
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "empty array", body: `{"tools":[]}`},
		{name: "parameter-less tool with name", body: `{"tools":[{"type":"function","function":{"name":"x"}}]}`},
		{name: "simple parameters", body: `{"tools":[` + toolWithParams(`{"type":"object","properties":{"city":{"type":"string"}}}`) + `]}`},
		{name: "two tools both valid", body: `{"tools":[` + toolWithParams(`{"type":"object"}`) + `,` + toolWithParams(`{"type":"object","properties":{"x":{"type":"number"}}}`) + `]}`},
		{name: "parameters at depth limit", body: `{"tools":[` + toolWithParams(nestedPropertiesSchema(16)) + `]}`},
		{name: "parameters at depth 12", body: `{"tools":[` + toolWithParams(nestedPropertiesSchema(12)) + `]}`},
		{name: "many properties at 200", body: `{"tools":[` + toolWithParams(manyPropertiesSchema(200)) + `]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		})
	}
}

func TestToolsValidatorRejects(t *testing.T) {
	v := defaultToolsValidator()
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "tools is object", body: `{"tools":{"x":1}}`, wantErr: ErrToolsShape},
		{name: "tools element is not object", body: `{"tools":["x"]}`, wantErr: ErrToolShape},
		// OpenAI tool contract: type must declare "function".
		{name: "type missing", body: `{"tools":[{"function":{"name":"x"}}]}`, wantErr: ErrToolFunctionType},
		{name: "type not a string", body: `{"tools":[{"type":1,"function":{"name":"x"}}]}`, wantErr: ErrToolFunctionType},
		{name: "type unknown value", body: `{"tools":[{"type":"plugin","function":{"name":"x"}}]}`, wantErr: ErrToolFunctionType},
		// function payload required.
		{name: "function missing", body: `{"tools":[{"type":"function"}]}`, wantErr: ErrToolFunctionShape},
		{name: "function not an object", body: `{"tools":[{"type":"function","function":"x"}]}`, wantErr: ErrToolFunctionShape},
		// function.name required.
		{name: "function name missing", body: `{"tools":[{"type":"function","function":{}}]}`, wantErr: ErrToolFunctionName},
		{name: "function name empty", body: `{"tools":[{"type":"function","function":{"name":""}}]}`, wantErr: ErrToolFunctionName},
		{name: "function name whitespace", body: `{"tools":[{"type":"function","function":{"name":"   "}}]}`, wantErr: ErrToolFunctionName},
		{name: "function name not a string", body: `{"tools":[{"type":"function","function":{"name":42}}]}`, wantErr: ErrToolFunctionName},
		{name: "depth exceeds limit", body: `{"tools":[` + toolWithParams(nestedPropertiesSchema(17)) + `]}`, wantErr: ErrSchemaDepth},
		{name: "deep recursion attack hidden in tool", body: `{"tools":[` + toolWithParams(nestedPropertiesSchema(200)) + `]}`, wantErr: ErrSchemaDepth},
		{name: "ref hidden in tool parameters", body: `{"tools":[` + toolWithParams(`{"$ref":"#/foo"}`) + `]}`, wantErr: ErrSchemaRef},
		{name: "ref hidden under if in tool", body: `{"tools":[` + toolWithParams(`{"if":{"$ref":"#/x"}}`) + `]}`, wantErr: ErrSchemaRef},
		// manyPropertiesSchema(256) = 1 root + 256 children = 257 nodes, one over.
		{name: "node count one over limit", body: `{"tools":[` + toolWithParams(manyPropertiesSchema(256)) + `]}`, wantErr: ErrSchemaNodes},
		{name: "size exceeds in tool", body: `{"tools":[` + toolWithParams(`{"type":"object","properties":{"`+strings.Repeat("a", 17*1024)+`":{"type":"string"}}}`) + `]}`, wantErr: ErrSchemaSize},
		{name: "anyOf exceeds in tool", body: `{"tools":[` + toolWithParams(`{"anyOf":[`+strings.Repeat(`{"type":"string"},`, 16)+`{"type":"string"}]}`) + `]}`, wantErr: ErrSchemaBranch},
		{name: "enum exceeds in tool", body: `{"tools":[` + toolWithParams(bigEnumSchema(257)) + `]}`, wantErr: ErrSchemaEnum},
		// Second tool is the bad one -- verifies we walk every element, not just the first.
		{name: "rejects bad schema in second tool", body: `{"tools":[` + toolWithParams(`{"type":"object"}`) + `,` + toolWithParams(`{"$ref":"#/x"}`) + `]}`, wantErr: ErrSchemaRef},
		// CVE-2025-48944: same xgrammar / regex crash class on the tools schema path.
		{name: "bad type in tool parameters", body: `{"tools":[` + toolWithParams(`{"type":"something"}`) + `]}`, wantErr: ErrSchemaType},
		{name: "bad pattern in tool parameters", body: `{"tools":[` + toolWithParams(`{"type":"string","pattern":"("}`) + `]}`, wantErr: ErrSchemaPattern},
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

func TestToolsValidatorStripsBothWhenToolsEmpty(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[],"tool_choice":"auto"}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.NotContains(t, doc, "tools")
	require.NotContains(t, doc, "tool_choice")
}

func TestToolsValidatorStripsBothWhenToolsEmptyEvenWithBadToolChoice(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[],"tool_choice":"required"}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.NotContains(t, doc, "tools")
	require.NotContains(t, doc, "tool_choice")
}

func TestToolsValidatorDefaultsToolChoiceToAutoWhenAbsent(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.Equal(t, "auto", doc["tool_choice"])
}

func TestToolsValidatorCoercesRequiredToDefault(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"required"}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.Equal(t, "auto", doc["tool_choice"])
}

func TestToolsValidatorCoercesRequiredEvenWithoutTools(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tool_choice":"required"}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.Equal(t, "auto", doc["tool_choice"])
}

func TestToolsValidatorDoesNotOverrideExplicitToolChoice(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[{"type":"function","function":{"name":"x"}}],"tool_choice":"none"}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.Equal(t, "none", doc["tool_choice"])
}

func TestToolsValidatorDoesNotTouchToolChoiceWhenToolsAbsent(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"messages":[]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.NotContains(t, doc, "tool_choice")
}

func TestToolsValidatorStripsFunctionStrict(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[{"type":"function","function":{"name":"x","strict":true,"parameters":{"type":"object"}}}]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	tools := doc["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	require.NotContains(t, fn, "strict")
	require.Equal(t, "x", fn["name"])
	require.Contains(t, fn, "parameters")
}

func TestToolsValidatorStripsFunctionStrictAcrossMultipleTools(t *testing.T) {
	v := defaultToolsValidator()
	doc := parseDocument(t, `{"tools":[{"type":"function","function":{"name":"a","strict":true}},{"type":"function","function":{"name":"b","strict":false}}]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	tools := doc["tools"].([]any)
	for _, t0 := range tools {
		fn := t0.(map[string]any)["function"].(map[string]any)
		require.NotContains(t, fn, "strict")
	}
}
