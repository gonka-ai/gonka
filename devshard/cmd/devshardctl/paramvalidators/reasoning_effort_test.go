package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReasoningEffortValidatorAccepts(t *testing.T) {
	v := ReasoningEffortValidator{}
	for _, value := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		t.Run(value, func(t *testing.T) {
			doc := parseDocument(t, `{"reasoning_effort":"`+value+`"}`)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			require.Equal(t, value, doc["reasoning_effort"], "validator must not mutate the field")
		})
	}
}

func TestReasoningEffortValidatorAbsent(t *testing.T) {
	v := ReasoningEffortValidator{}
	doc := parseDocument(t, `{"messages":[]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
}

func TestReasoningEffortValidatorRejects(t *testing.T) {
	v := ReasoningEffortValidator{}
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "not a string (number)", body: `{"reasoning_effort":5}`, wantErr: ErrReasoningEffortShape},
		{name: "not a string (bool)", body: `{"reasoning_effort":true}`, wantErr: ErrReasoningEffortShape},
		{name: "not a string (null)", body: `{"reasoning_effort":null}`, wantErr: ErrReasoningEffortShape},
		{name: "not a string (object)", body: `{"reasoning_effort":{"effort":"high"}}`, wantErr: ErrReasoningEffortShape},
		{name: "unknown enum value", body: `{"reasoning_effort":"max"}`, wantErr: ErrReasoningEffortValue},
		{name: "wrong case", body: `{"reasoning_effort":"High"}`, wantErr: ErrReasoningEffortValue},
		{name: "empty string", body: `{"reasoning_effort":""}`, wantErr: ErrReasoningEffortValue},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tt.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
