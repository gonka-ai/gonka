package paramvalidators

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKimiMaxTokensFloorValidator(t *testing.T) {
	v := KimiMaxTokensFloorValidator{Model: "moonshotai/Kimi-K2.6", Min: 16}

	t.Run("lifts max_tokens below floor", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":1}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: v.Model}))
		require.EqualValues(t, 16, doc["max_tokens"])
	})

	t.Run("lifts max_completion_tokens below floor", func(t *testing.T) {
		doc := parseDocument(t, `{"max_completion_tokens":8}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: v.Model}))
		require.EqualValues(t, 16, doc["max_completion_tokens"])
	})

	t.Run("lifts both fields when both below floor", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":4,"max_completion_tokens":8}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: v.Model}))
		require.EqualValues(t, 16, doc["max_tokens"])
		require.EqualValues(t, 16, doc["max_completion_tokens"])
	})

	t.Run("leaves values at or above floor", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":16,"max_completion_tokens":100}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: v.Model}))
		require.Equal(t, json.Number("16"), doc["max_tokens"])
		require.Equal(t, json.Number("100"), doc["max_completion_tokens"])
	})

	t.Run("skips missing fields", func(t *testing.T) {
		doc := parseDocument(t, `{}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: v.Model}))
		_, hasMax := doc["max_tokens"]
		require.False(t, hasMax)
	})

	t.Run("no-op for other models", func(t *testing.T) {
		doc := parseDocument(t, `{"max_tokens":1}`)
		require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "some/other-model"}))
		require.Equal(t, json.Number("1"), doc["max_tokens"])
	})
}
