package paramvalidators

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGreedySamplingValidatorCoercesNToOne(t *testing.T) {
	v := GreedySamplingValidator{}
	cases := []struct {
		name string
		body string
	}{
		{"json.Number n + json.Number temp", `{"n":5,"temperature":0}`},
		{"json.Number n + float temp", `{"n":3,"temperature":0.0}`},
		{"uint64 n", ""}, // typed below
	}
	for _, tc := range cases {
		if tc.name == "uint64 n" {
			doc := map[string]any{"n": uint64(5), "temperature": 0.0}
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			require.Equal(t, uint64(1), doc["n"])
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			doc := parseDocument(t, tc.body)
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			require.Equal(t, uint64(1), doc["n"])
		})
	}
}

func TestGreedySamplingValidatorLeavesNAlone(t *testing.T) {
	v := GreedySamplingValidator{}
	cases := []struct {
		name string
		body string
	}{
		{"n absent", `{"temperature":0}`},
		{"n is 1", `{"n":1,"temperature":0}`},
		{"temperature absent", `{"n":5}`},
		{"temperature non-zero", `{"n":5,"temperature":0.7}`},
		{"temperature exactly tiny positive", `{"n":5,"temperature":0.0001}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseDocument(t, tc.body)
			before, _ := doc["n"]
			require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
			if before == nil {
				require.NotContains(t, doc, "n")
				return
			}
			// We expect untouched.
			require.Equal(t, before, doc["n"])
		})
	}
}

func TestGreedySamplingValidatorWithStringTemperature(t *testing.T) {
	// PostLimits runs after SanitizeFloatParameterHandler, but unit-level we exercise the
	// raw-string branch of numericAsFloat64 (string "0" still triggers the coerce).
	v := GreedySamplingValidator{}
	doc := map[string]any{"n": json.Number("5"), "temperature": "0"}
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
	require.Equal(t, uint64(1), doc["n"])
}
