package paramvalidators

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultKimiTTBValidator() KimiThinkingTokenBudgetValidator {
	return KimiThinkingTokenBudgetValidator{
		Model:                   "moonshotai/Kimi-K2.6",
		DefaultDivisor:          2,
		AbsoluteMax:             96_000,
		ContentHeadroom:         64,
		ForceZeroBelowMaxTokens: 256,
	}
}

func TestKimiThinkingTokenBudgetValidator(t *testing.T) {
	v := defaultKimiTTBValidator()
	ctx := func(doc map[string]any) ValidatorContext {
		return ValidatorContext{Document: doc, RoutedModel: v.Model}
	}

	t.Run("defaults to half when ttb absent and max_tokens above force-zero", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4096}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 2048, doc["thinking_token_budget"])
	})

	t.Run("force-zero at small max_tokens overrides client value", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":100,"thinking_token_budget":50}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 0, doc["thinking_token_budget"])
	})

	t.Run("boundary at force-zero threshold keeps half split", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":256}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 128, doc["thinking_token_budget"])
	})

	t.Run("content headroom clamps above max_tokens minus headroom", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4096,"thinking_token_budget":10000}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 4032, doc["thinking_token_budget"])
	})

	t.Run("absolute max caps very large ttb", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":200000,"thinking_token_budget":150000}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 96_000, doc["thinking_token_budget"])
	})

	t.Run("preserves client ttb under headroom", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4096,"thinking_token_budget":500}`)
		require.NoError(t, v.Validate(ctx(doc)))
		require.EqualValues(t, 500, doc["thinking_token_budget"])
	})

	t.Run("skips when max_tokens absent", func(t *testing.T) {
		doc := parseDocument(t, `{}`)
		require.NoError(t, v.Validate(ctx(doc)))
		_, has := doc["thinking_token_budget"]
		require.False(t, has)
	})

	t.Run("skips when max_tokens is zero", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":0}`)
		require.NoError(t, v.Validate(ctx(doc)))
		_, has := doc["thinking_token_budget"]
		require.False(t, has)
	})

	t.Run("no-op for other models", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":100,"thinking_token_budget":50}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "some/other-model"}))
		require.Equal(t, json.Number("50"), doc["thinking_token_budget"])
	})
}
