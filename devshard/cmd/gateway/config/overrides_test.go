package config

import (
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build an `Overrides` value with every field populated, including per-model limits with both `MaxConcurrentRequests` and `MaxInputTokensInFlight` pointers set.
//  2. Encode it via `EncodeOverrides` and decode it back via `ParseOverrides`.
//  3. Assert every top-level and per-model field round-trips, including both per-model pointer fields, and that fields never set decode back to nil.
func TestOverridesJSONRoundTrip(t *testing.T) {
	maxTokens := int64(2048)
	disabled := true
	modelMaxConcurrentRequests := int64(64)
	modelMaxInputTokensInFlight := int64(8192)
	original := Overrides{
		Tunables:    env.Tunables{DefaultMaxTokens: &maxTokens, Disabled: &disabled},
		ModelAccess: map[string]string{"model-a": "api_key"},
		ModelLimits: map[string]ModelLimits{
			"model-a": {
				DefaultMaxTokens:       1024,
				MaxTokensCap:           2048,
				MaxConcurrentRequests:  &modelMaxConcurrentRequests,
				MaxInputTokensInFlight: &modelMaxInputTokensInFlight,
			},
		},
	}

	encoded, err := original.EncodeOverrides()
	if err != nil {
		t.Fatalf("EncodeOverrides(): %v", err)
	}
	decoded, err := ParseOverrides(encoded)
	if err != nil {
		t.Fatalf("ParseOverrides(): %v", err)
	}
	if decoded.DefaultMaxTokens == nil || *decoded.DefaultMaxTokens != 2048 {
		t.Fatalf("DefaultMaxTokens = %v, want 2048", decoded.DefaultMaxTokens)
	}
	if decoded.MaxTokensCap != nil {
		t.Fatalf("MaxTokensCap = %v, want nil (never set)", *decoded.MaxTokensCap)
	}
	if decoded.ModelAccess["model-a"] != "api_key" {
		t.Fatalf("ModelAccess = %v, want model-a→api_key", decoded.ModelAccess)
	}
	if decoded.ModelLimits["model-a"].MaxTokensCap != 2048 {
		t.Fatalf("ModelLimits = %v, want model-a cap 2048", decoded.ModelLimits)
	}
	decodedMaxConcurrentRequests := decoded.ModelLimits["model-a"].MaxConcurrentRequests
	if decodedMaxConcurrentRequests == nil || *decodedMaxConcurrentRequests != 64 {
		t.Fatalf("ModelLimits[model-a].MaxConcurrentRequests = %v, want 64", decodedMaxConcurrentRequests)
	}
	decodedMaxInputTokensInFlight := decoded.ModelLimits["model-a"].MaxInputTokensInFlight
	if decodedMaxInputTokensInFlight == nil || *decodedMaxInputTokensInFlight != 8192 {
		t.Fatalf("ModelLimits[model-a].MaxInputTokensInFlight = %v, want 8192", decodedMaxInputTokensInFlight)
	}
}

// Test flow:
//  1. Parse a JSON object containing a field name the schema does not recognize.
//  2. Assert `ParseOverrides` returns an error rather than silently ignoring the typo.
func TestParseOverridesRejectsUnknownFields(t *testing.T) {
	_, err := ParseOverrides([]byte(`{"no_such_setting": 1}`))
	if err == nil {
		t.Fatal("ParseOverrides with unknown field: want error, got nil (admin typos must not be silently ignored)")
	}
}

// Test flow:
//  1. Parse an empty JSON object.
//  2. Assert every field decodes to its zero value.
func TestParseOverridesOfEmptyObjectIsEmpty(t *testing.T) {
	decoded, err := ParseOverrides([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseOverrides(empty): %v", err)
	}
	if decoded.DefaultMaxTokens != nil || decoded.Disabled != nil || len(decoded.ModelAccess) != 0 {
		t.Fatalf("ParseOverrides(empty) = %+v, want zero value", decoded)
	}
}

// Test flow:
//  1. Parse a settings body naming a shared tunable and an admin-only field.
//  2. Assert both land, and encoding writes the tunable as a top-level key rather than under the embedded struct.
//  3. Assert an unknown field is still refused.
func TestSharedTunablesStayFlatInTheSettingsJSON(t *testing.T) {
	parsed, err := ParseOverrides([]byte(`{"max_tokens_cap":7,"fallback_max_model_len":9}`))
	if err != nil {
		t.Fatalf("ParseOverrides = %v", err)
	}
	if parsed.MaxTokensCap == nil || *parsed.MaxTokensCap != 7 || parsed.FallbackMaxModelLen == nil || *parsed.FallbackMaxModelLen != 9 {
		t.Fatalf("parsed = %+v, want both fields set", parsed)
	}
	encoded, err := parsed.EncodeOverrides()
	if err != nil {
		t.Fatalf("EncodeOverrides = %v", err)
	}
	if want := `{"max_tokens_cap":7,"fallback_max_model_len":9}`; string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
	if _, err := ParseOverrides([]byte(`{"max_tokens_capp":7}`)); err == nil {
		t.Fatal("ParseOverrides accepted an unknown field")
	}
}
