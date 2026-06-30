package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultToolChoiceValidator() ToolChoiceValidator {
	return ToolChoiceValidator{MaxNameLen: 64}
}

func TestToolChoiceValidatorAccepts(t *testing.T) {
	v := defaultToolChoiceValidator()
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "auto", body: `{"tool_choice":"auto"}`},
		{name: "none", body: `{"tool_choice":"none"}`},
		{name: "function object", body: `{"tool_choice":{"type":"function","function":{"name":"web_search"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)}))
		})
	}
}

func TestToolChoiceValidatorRejects(t *testing.T) {
	v := defaultToolChoiceValidator()
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "unknown string", body: `{"tool_choice":"force"}`, wantErr: ErrToolChoiceShape},
		{name: "required reaches validator without upstream coerce", body: `{"tool_choice":"required"}`, wantErr: ErrToolChoiceShape},
		{name: "number", body: `{"tool_choice":42}`, wantErr: ErrToolChoiceShape},
		{name: "boolean", body: `{"tool_choice":true}`, wantErr: ErrToolChoiceShape},
		{name: "array", body: `{"tool_choice":["auto"]}`, wantErr: ErrToolChoiceShape},
		{name: "object missing type", body: `{"tool_choice":{"function":{"name":"x"}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object wrong type", body: `{"tool_choice":{"type":"plugin","function":{"name":"x"}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object missing function", body: `{"tool_choice":{"type":"function"}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object function not object", body: `{"tool_choice":{"type":"function","function":"x"}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object missing name", body: `{"tool_choice":{"type":"function","function":{}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object empty name", body: `{"tool_choice":{"type":"function","function":{"name":""}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object whitespace name", body: `{"tool_choice":{"type":"function","function":{"name":"   "}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "object non-string name", body: `{"tool_choice":{"type":"function","function":{"name":42}}}`, wantErr: ErrToolChoiceFunctionShape},
		{name: "name exceeds length cap", body: `{"tool_choice":{"type":"function","function":{"name":"` + longString(65) + `"}}}`, wantErr: ErrToolChoiceFunctionShape},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func longString(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}
