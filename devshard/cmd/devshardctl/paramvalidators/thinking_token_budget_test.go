package paramvalidators

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThinkingTokenBudgetDefaultsValidator(t *testing.T) {
	v := ThinkingTokenBudgetDefaultsValidator{DefaultDivisor: 2}

	t.Run("injects default when absent", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4096}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		require.EqualValues(t, 2048, doc["thinking_token_budget"])
	})

	t.Run("preserves client value when present", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4096,"thinking_token_budget":500}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		require.Equal(t, json.Number("500"), doc["thinking_token_budget"])
	})

	t.Run("splits in half regardless of size", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":400}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		require.EqualValues(t, 200, doc["thinking_token_budget"])
	})

	t.Run("small max_tokens still leaves content room", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":100}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		require.EqualValues(t, 50, doc["thinking_token_budget"])
	})

	t.Run("skips when max_tokens absent", func(t *testing.T) {
		doc := parseDocument(t, `{}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		_, exists := doc["thinking_token_budget"]
		require.False(t, exists)
	})

	t.Run("skips when max_tokens is zero", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":0}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
		_, exists := doc["thinking_token_budget"]
		require.False(t, exists)
	})

	t.Run("zero divisor uses max_tokens", func(t *testing.T) {
		vZero := ThinkingTokenBudgetDefaultsValidator{DefaultDivisor: 0}
		doc := parseDocument(t, `{"max_tokens":4096}`)
		require.NoError(t, vZero.Validate(ValidatorContext{Document: doc}))
		require.EqualValues(t, 4096, doc["thinking_token_budget"])
	})
}
