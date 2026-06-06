package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnableThinkingValidatorTranslatesToChatTemplateKwargs(t *testing.T) {
	v := EnableThinkingValidator{}
	cases := []struct {
		name  string
		body  string
		value bool
	}{
		{name: "true", body: `{"enable_thinking":true}`, value: true},
		{name: "false", body: `{"enable_thinking":false}`, value: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			require.NotContains(t, doc, "enable_thinking")
			kwargs, ok := doc["chat_template_kwargs"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tt.value, kwargs["enable_thinking"])
		})
	}
}

func TestEnableThinkingValidatorPreservesExistingNestedValue(t *testing.T) {
	v := EnableThinkingValidator{}
	doc := parseDocument(t, `{"enable_thinking":true,"chat_template_kwargs":{"enable_thinking":false}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "enable_thinking")
	kwargs, _ := doc["chat_template_kwargs"].(map[string]any)
	require.Equal(t, false, kwargs["enable_thinking"], "pre-existing chat_template_kwargs.enable_thinking wins")
}

func TestEnableThinkingValidatorPreservesOtherKwargs(t *testing.T) {
	v := EnableThinkingValidator{}
	doc := parseDocument(t, `{"enable_thinking":true,"chat_template_kwargs":{"foo":"bar"}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	kwargs, _ := doc["chat_template_kwargs"].(map[string]any)
	require.Equal(t, true, kwargs["enable_thinking"])
	require.Equal(t, "bar", kwargs["foo"])
}

func TestEnableThinkingValidatorRejects(t *testing.T) {
	v := EnableThinkingValidator{}
	cases := []struct {
		name string
		body string
	}{
		{name: "string true", body: `{"enable_thinking":"true"}`},
		{name: "number", body: `{"enable_thinking":1}`},
		{name: "null", body: `{"enable_thinking":null}`},
		{name: "object", body: `{"enable_thinking":{}}`},
		{name: "array", body: `{"enable_thinking":[]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEnableThinkingShape)
		})
	}
}

func TestEnableThinkingValidatorAbsent(t *testing.T) {
	v := EnableThinkingValidator{}
	doc := parseDocument(t, `{"messages":[]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.NotContains(t, doc, "chat_template_kwargs")
}

func TestEnableThinkingValidatorRejectsWrongShapeChatTemplateKwargs(t *testing.T) {
	v := EnableThinkingValidator{}
	cases := []struct {
		name string
		body string
	}{
		{name: "string", body: `{"enable_thinking":true,"chat_template_kwargs":"broken"}`},
		{name: "array", body: `{"enable_thinking":true,"chat_template_kwargs":[1,2]}`},
		{name: "number", body: `{"enable_thinking":true,"chat_template_kwargs":42}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			err := v.Validate(ValidatorContext{Document: doc})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrChatTemplateKwargsShape)
			require.Contains(t, doc, "enable_thinking", "top-level field must be preserved when we cannot translate it safely")
		})
	}
}
