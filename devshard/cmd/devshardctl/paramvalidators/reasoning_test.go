package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReasoningValidatorLiftsEffort(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning":{"effort":"high"}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "reasoning")
	require.Equal(t, "high", doc["reasoning_effort"])
}

func TestReasoningValidatorDropsNonEffortKeys(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning":{"effort":"medium","max_tokens":2000,"exclude":true}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "reasoning")
	require.NotContains(t, doc, "max_tokens")
	require.NotContains(t, doc, "exclude")
	require.Equal(t, "medium", doc["reasoning_effort"])
}

func TestReasoningValidatorEnabledFalseDropsEverything(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning":{"enabled":false,"effort":"high"}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "reasoning")
	require.NotContains(t, doc, "reasoning_effort", "enabled:false must override any effort")
}

func TestReasoningValidatorEnabledTrueLiftsEffort(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning":{"enabled":true,"effort":"low"}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.Equal(t, "low", doc["reasoning_effort"])
}

func TestReasoningValidatorTopLevelEffortWins(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning_effort":"medium","reasoning":{"effort":"high"}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "reasoning")
	require.Equal(t, "medium", doc["reasoning_effort"], "pre-existing top-level reasoning_effort wins")
}

func TestReasoningValidatorEmptyObjectJustDrops(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"reasoning":{}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	require.NotContains(t, doc, "reasoning")
	require.NotContains(t, doc, "reasoning_effort")
}

func TestReasoningValidatorSilentStripsNonObject(t *testing.T) {
	v := ReasoningValidator{}
	cases := []struct {
		name string
		body string
	}{
		{name: "string", body: `{"reasoning":"high"}`},
		{name: "number", body: `{"reasoning":42}`},
		{name: "bool", body: `{"reasoning":true}`},
		{name: "null", body: `{"reasoning":null}`},
		{name: "array", body: `{"reasoning":[{"effort":"high"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseDocument(t, tc.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			require.NotContains(t, doc, "reasoning")
			require.NotContains(t, doc, "reasoning_effort")
		})
	}
}

func TestReasoningValidatorAbsent(t *testing.T) {
	v := ReasoningValidator{}
	doc := parseDocument(t, `{"messages":[]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
}
