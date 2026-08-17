package paramvalidators

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func defaultStructuredOutputsValidator() StructuredOutputsValidator {
	return StructuredOutputsValidator{
		RejectedModels:      []string{"kimi-model"},
		MaxDepth:            16,
		MaxSize:             16 * 1024,
		MaxNodes:            128,
		MaxBranch:           16,
		MaxEnum:             256,
		MaxPatternLen:       512,
		MaxChoiceEntries:    256,
		MaxChoiceEntryLen:   1024,
		MaxGrammarLen:       8 * 1024,
		MaxGrammarNesting:   200,
		MaxStructuralTagLen: 4 * 1024,
	}
}

func TestStructuredOutputsValidatorAbsent(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	doc := parseDocument(t, `{"messages":[]}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))
}

func TestStructuredOutputsValidatorRejectsOnGatedRoute(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	doc := parseDocument(t, `{"structured_outputs":{"json":{"type":"object"}}}`)

	err := v.Validate(ValidatorContext{Document: doc, RoutedModel: "kimi-model"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStructuredOutputsNotSupportedOnRoute)
}

func TestStructuredOutputsValidatorAcceptsOnNonGatedRoute(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	doc := parseDocument(t, `{"structured_outputs":{"json":{"type":"object"}}}`)
	require.NoError(t, v.Validate(ValidatorContext{Document: doc, RoutedModel: "qwen-model"}))
}

func TestStructuredOutputsValidatorRejectsNonObjectWrapper(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	cases := []string{
		`{"structured_outputs":"x"}`,
		`{"structured_outputs":42}`,
		`{"structured_outputs":null}`,
		`{"structured_outputs":[]}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrStructuredOutputsShape)
		})
	}
}

func TestStructuredOutputsValidatorRejectsResponseFormatConflict(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	doc := parseDocument(t, `{"response_format":{"type":"json_object"},"structured_outputs":{"json_object":true}}`)

	err := v.Validate(ValidatorContext{Document: doc})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStructuredOutputsResponseFormatConflict)
}

func TestStructuredOutputsValidatorEnforcesExactlyOne(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	cases := []struct {
		name string
		body string
	}{
		{name: "empty envelope", body: `{"structured_outputs":{}}`},
		{name: "zero constraints", body: `{"structured_outputs":{"disable_any_whitespace":true}}`},
		{name: "two constraints", body: `{"structured_outputs":{"json":{"type":"string"},"regex":"\\d+"}}`},
		{name: "all six constraints", body: `{"structured_outputs":{"json":{"type":"string"},"regex":"x","choice":["a"],"grammar":"r:a","json_object":true,"structural_tag":"<x/>"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(ValidatorContext{Document: parseDocument(t, tc.body)})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrStructuredOutputsExactlyOne)
		})
	}
}

func TestStructuredOutputsValidatorStripsPrivateFields(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	doc := parseDocument(t, `{"structured_outputs":{"json_object":true,"_backend":"xgrammar","_backend_was_auto":true}}`)

	require.NoError(t, v.Validate(ValidatorContext{Document: doc}))

	so, _ := doc["structured_outputs"].(map[string]any)
	require.NotContains(t, so, "_backend")
	require.NotContains(t, so, "_backend_was_auto")
}

func TestStructuredOutputsValidatorJSONAcceptsBoundedSchema(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	body := `{"structured_outputs":{"json":{"type":"object","properties":{"x":{"type":"string"}}}}}`
	require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
}

func TestStructuredOutputsValidatorJSONRejectsStringForm(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	body := `{"structured_outputs":{"json":"{\"type\":\"object\"}"}}`

	err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStructuredOutputsJSONShape)
}

func TestStructuredOutputsValidatorJSONRejectsRef(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	body := `{"structured_outputs":{"json":{"type":"object","properties":{"x":{"$ref":"#/definitions/x"}}}}}`

	err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "structured_outputs.json")
}

func TestStructuredOutputsValidatorRegex(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts a normal pattern", func(t *testing.T) {
		body := `{"structured_outputs":{"regex":"^[a-z]+$"}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects non-string", func(t *testing.T) {
		body := `{"structured_outputs":{"regex":42}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsRegexShape)
	})

	t.Run("rejects over-length", func(t *testing.T) {
		long := strings.Repeat("a", 513)
		body := `{"structured_outputs":{"regex":"` + long + `"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsRegexLength)
	})

	t.Run("rejects uncompilable", func(t *testing.T) {
		body := `{"structured_outputs":{"regex":"["}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsRegexCompile)
	})
}

func TestStructuredOutputsValidatorChoice(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts bounded list", func(t *testing.T) {
		body := `{"structured_outputs":{"choice":["yes","no","maybe"]}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects empty array", func(t *testing.T) {
		body := `{"structured_outputs":{"choice":[]}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceShape)
	})

	t.Run("rejects non-array", func(t *testing.T) {
		body := `{"structured_outputs":{"choice":"yes"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceShape)
	})

	t.Run("rejects non-string item", func(t *testing.T) {
		body := `{"structured_outputs":{"choice":["a",42]}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceShape)
	})

	t.Run("rejects too many entries", func(t *testing.T) {
		entries := make([]string, 257)
		for i := range entries {
			entries[i] = `"x"`
		}
		body := `{"structured_outputs":{"choice":[` + strings.Join(entries, ",") + `]}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceLimit)
	})

	t.Run("rejects over-long entry", func(t *testing.T) {
		long := strings.Repeat("a", 1025)
		body := `{"structured_outputs":{"choice":["` + long + `"]}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceLimit)
	})

	t.Run("rejects when total length exceeds MaxSize", func(t *testing.T) {
		// 20 entries × 1024 bytes = 20 KiB > 16 KiB MaxSize.
		entry := `"` + strings.Repeat("a", 1024) + `"`
		entries := make([]string, 20)
		for i := range entries {
			entries[i] = entry
		}
		body := `{"structured_outputs":{"choice":[` + strings.Join(entries, ",") + `]}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsChoiceLimit)
	})
}

func TestStructuredOutputsValidatorGrammar(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts simple grammar", func(t *testing.T) {
		body := `{"structured_outputs":{"grammar":"start: \"hello\""}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects non-string", func(t *testing.T) {
		body := `{"structured_outputs":{"grammar":{}}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsGrammarShape)
	})

	t.Run("rejects oversize string", func(t *testing.T) {
		long := strings.Repeat("a", 8*1024+1)
		body := `{"structured_outputs":{"grammar":"` + long + `"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsGrammarLength)
	})

	t.Run("rejects deep nesting (CVE-2026-25048 PoC pattern)", func(t *testing.T) {
		bombs := strings.Repeat("(", 201)
		body := `{"structured_outputs":{"grammar":"` + bombs + `"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsGrammarNesting)
	})

	t.Run("balanced brackets do not accumulate depth", func(t *testing.T) {
		// 250 paired brackets — depth never goes above 1.
		body := `{"structured_outputs":{"grammar":"` + strings.Repeat("()", 250) + `"}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})
}

func TestStructuredOutputsValidatorJSONObject(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts true", func(t *testing.T) {
		body := `{"structured_outputs":{"json_object":true}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("accepts false", func(t *testing.T) {
		body := `{"structured_outputs":{"json_object":false}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects non-bool", func(t *testing.T) {
		body := `{"structured_outputs":{"json_object":"true"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsJSONObjectShape)
	})
}

func TestStructuredOutputsValidatorStructuralTag(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts the object form", func(t *testing.T) {
		body := `{"structured_outputs":{"structural_tag":{"type":"structural_tag","structures":[],"triggers":[]}}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects the engine-crashing string form", func(t *testing.T) {
		// A JSON-encoded string structural_tag returns HTTP 500 from vLLM; reject it here.
		body := `{"structured_outputs":{"structural_tag":"<tool>...</tool>"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsStructuralTagShape)
	})

	t.Run("rejects a non-object scalar", func(t *testing.T) {
		body := `{"structured_outputs":{"structural_tag":42}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsStructuralTagShape)
	})

	t.Run("rejects oversize object", func(t *testing.T) {
		long := strings.Repeat("a", 4*1024+1)
		body := `{"structured_outputs":{"structural_tag":{"type":"structural_tag","blob":"` + long + `"}}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsStructuralTagLength)
	})
}

func TestStructuredOutputsValidatorWhitespacePattern(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts a pattern alongside json", func(t *testing.T) {
		body := `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":"\\s*"}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects over-length", func(t *testing.T) {
		long := strings.Repeat("a", 513)
		body := `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":"` + long + `"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsWhitespacePatternLength)
	})

	t.Run("rejects uncompilable", func(t *testing.T) {
		body := `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":"["}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsWhitespacePatternCompile)
	})
}

func TestStructuredOutputsValidatorBoolFlags(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("accepts bools on flags", func(t *testing.T) {
		body := `{"structured_outputs":{"json_object":true,"disable_any_whitespace":true,"disable_additional_properties":false}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("rejects non-bool flag", func(t *testing.T) {
		body := `{"structured_outputs":{"json_object":true,"disable_any_whitespace":"yes"}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsBoolFlagShape)
	})
}

func TestStructuredOutputsValidatorRejectsUnknownSubField(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	body := `{"structured_outputs":{"json_object":true,"backend":"outlines"}}`
	err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
	require.ErrorIs(t, err, ErrStructuredOutputsShape)
	require.Contains(t, err.Error(), "backend")
}

func TestStructuredOutputsValidatorTreatsExplicitNullAsAbsent(t *testing.T) {
	v := defaultStructuredOutputsValidator()

	t.Run("null + one set => count is 1", func(t *testing.T) {
		body := `{"structured_outputs":{"json":null,"regex":"\\d+"}}`
		require.NoError(t, v.Validate(ValidatorContext{Document: parseDocument(t, body)}))
	})

	t.Run("all six null => count is 0", func(t *testing.T) {
		body := `{"structured_outputs":{"json":null,"regex":null,"choice":null,"grammar":null,"json_object":null,"structural_tag":null}}`
		err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
		require.ErrorIs(t, err, ErrStructuredOutputsExactlyOne)
		require.Contains(t, err.Error(), "got 0")
	})
}

func TestStructuredOutputsValidatorExactlyOneErrorListsSetFields(t *testing.T) {
	v := defaultStructuredOutputsValidator()
	body := `{"structured_outputs":{"json":{"type":"string"},"regex":"\\d+"}}`
	err := v.Validate(ValidatorContext{Document: parseDocument(t, body)})
	require.ErrorIs(t, err, ErrStructuredOutputsExactlyOne)
	require.Contains(t, err.Error(), "json")
	require.Contains(t, err.Error(), "regex")
}
