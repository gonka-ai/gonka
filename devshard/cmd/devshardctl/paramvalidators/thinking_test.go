package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThinkingValidatorAccepts(t *testing.T) {
	v := ThinkingValidator{}
	tests := []struct {
		name string
		body string
	}{
		{name: "absent", body: `{"messages":[]}`},
		{name: "enabled", body: `{"thinking":{"type":"enabled"}}`},
		{name: "disabled", body: `{"thinking":{"type":"disabled"}}`},
		{name: "adaptive", body: `{"thinking":{"type":"adaptive"}}`},
		{name: "auto", body: `{"thinking":{"type":"auto"}}`},
		{name: "adaptive with display hint", body: `{"thinking":{"type":"adaptive","display":"summarized"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)}))
		})
	}
}

func TestThinkingValidatorNormalizesClientExtensions(t *testing.T) {
	v := ThinkingValidator{}
	tests := []struct {
		name     string
		body     string
		wantType string
	}{
		{name: "adaptive normalizes to enabled", body: `{"thinking":{"type":"adaptive"}}`, wantType: "enabled"},
		{name: "auto normalizes to enabled", body: `{"thinking":{"type":"auto"}}`, wantType: "enabled"},
		{name: "enabled stays enabled", body: `{"thinking":{"type":"enabled"}}`, wantType: "enabled"},
		{name: "disabled stays disabled", body: `{"thinking":{"type":"disabled"}}`, wantType: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			thinking, ok := doc["thinking"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tt.wantType, thinking["type"])
		})
	}
}

func TestThinkingValidatorStripsDisplayHint(t *testing.T) {
	v := ThinkingValidator{}
	doc := parseDocument(t, `{"thinking":{"type":"adaptive","display":"summarized"}}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	thinking, ok := doc["thinking"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, thinking, "display", "display is a CLI UI hint and must be stripped")
}

func TestThinkingValidatorAdaptiveMirrorsToTemplateKwargs(t *testing.T) {
	v := ThinkingValidator{MirrorToTemplateKwargsForModels: []string{"kimi-model"}}
	doc := parseDocument(t, `{"thinking":{"type":"adaptive","display":"summarized"}}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
	kwargs, ok := doc["chat_template_kwargs"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, kwargs["thinking"], "adaptive must mirror as enabled for Kimi")
	require.NotContains(t, doc, "thinking", "top-level thinking is dropped after mirror; vLLM chat template only reads from chat_template_kwargs.thinking")
}

func TestThinkingValidatorStripsTopLevelAfterMirror(t *testing.T) {
	v := ThinkingValidator{MirrorToTemplateKwargsForModels: []string{"kimi-model"}}
	tests := []struct {
		name string
		body string
	}{
		{name: "enabled", body: `{"thinking":{"type":"enabled"}}`},
		{name: "disabled", body: `{"thinking":{"type":"disabled"}}`},
		{name: "auto", body: `{"thinking":{"type":"auto"}}`},
		{name: "adaptive with display", body: `{"thinking":{"type":"adaptive","display":"summarized"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDocument(t, tt.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
			require.NotContains(t, doc, "thinking")
		})
	}
}

func TestThinkingValidatorKeepsTopLevelForNonMirrorModels(t *testing.T) {
	v := ThinkingValidator{MirrorToTemplateKwargsForModels: []string{"kimi-model"}}
	doc := parseDocument(t, `{"thinking":{"type":"adaptive","display":"summarized"}}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "other-model"}))
	thinking, ok := doc["thinking"].(map[string]any)
	require.True(t, ok, "non-mirror path retains top-level thinking for forward compat")
	require.Equal(t, "enabled", thinking["type"], "type normalized to canonical enum")
	require.NotContains(t, thinking, "display", "display sibling silent-stripped")
}

func TestThinkingValidatorMirrorsToTemplateKwargsForScopedModels(t *testing.T) {
	v := ThinkingValidator{MirrorToTemplateKwargsForModels: []string{"kimi-model"}}

	t.Run("mirrors enabled to chat_template_kwargs", func(t *testing.T) {
		doc := parseDocument(t, `{"thinking":{"type":"enabled"}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
		kwargs, ok := doc["chat_template_kwargs"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, kwargs["thinking"])
	})

	t.Run("mirrors disabled to chat_template_kwargs", func(t *testing.T) {
		doc := parseDocument(t, `{"thinking":{"type":"disabled"}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
		kwargs, ok := doc["chat_template_kwargs"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, false, kwargs["thinking"])
	})

	t.Run("preserves existing chat_template_kwargs.thinking", func(t *testing.T) {
		doc := parseDocument(t, `{"thinking":{"type":"enabled"},"chat_template_kwargs":{"thinking":false}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
		kwargs, _ := doc["chat_template_kwargs"].(map[string]any)
		require.Equal(t, false, kwargs["thinking"])
	})

	t.Run("preserves other chat_template_kwargs entries", func(t *testing.T) {
		doc := parseDocument(t, `{"thinking":{"type":"enabled"},"chat_template_kwargs":{"foo":"bar"}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
		kwargs, _ := doc["chat_template_kwargs"].(map[string]any)
		require.Equal(t, true, kwargs["thinking"])
		require.Equal(t, "bar", kwargs["foo"])
	})

	t.Run("does not mirror for other models", func(t *testing.T) {
		doc := parseDocument(t, `{"thinking":{"type":"enabled"}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "other-model"}))
		_, ok := doc["chat_template_kwargs"]
		require.False(t, ok)
	})

	t.Run("does not mirror when MirrorToTemplateKwargsForModels is empty", func(t *testing.T) {
		v := ThinkingValidator{}
		doc := parseDocument(t, `{"thinking":{"type":"enabled"}}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"}))
		_, ok := doc["chat_template_kwargs"]
		require.False(t, ok)
	})
}

func TestThinkingValidatorRejectsWrongShapeChatTemplateKwargs(t *testing.T) {
	v := ThinkingValidator{MirrorToTemplateKwargsForModels: []string{"kimi-model"}}
	doc := parseDocument(t, `{"thinking":{"type":"enabled"},"chat_template_kwargs":"broken"}`)

	err := v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrChatTemplateKwargsShape)
	require.Equal(t, "broken", doc["chat_template_kwargs"], "wrong-type value must not be overwritten by mirror")
}

func TestThinkingValidatorRejects(t *testing.T) {
	v := ThinkingValidator{}
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "wrapper not object", body: `{"thinking":"enabled"}`, wantErr: ErrThinkingShape},
		{name: "wrapper is array", body: `{"thinking":[]}`, wantErr: ErrThinkingShape},
		{name: "wrapper is bool", body: `{"thinking":true}`, wantErr: ErrThinkingShape},
		{name: "type missing", body: `{"thinking":{}}`, wantErr: ErrThinkingType},
		{name: "type is bool", body: `{"thinking":{"type":true}}`, wantErr: ErrThinkingType},
		{name: "type is unknown string", body: `{"thinking":{"type":"on"}}`, wantErr: ErrThinkingType},
		{name: "type is empty string", body: `{"thinking":{"type":""}}`, wantErr: ErrThinkingType},
		{name: "type is gibberish", body: `{"thinking":{"type":"sometimes"}}`, wantErr: ErrThinkingType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
