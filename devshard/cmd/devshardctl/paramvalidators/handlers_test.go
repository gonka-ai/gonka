package paramvalidators

import (
	"math"
	"strings"
	"testing"

	"devshard/cmd/devshardctl/testutil"
)

// run is a tiny wrapper that builds a ParameterContext and dispatches the handler.
func run(h ParameterHandler, doc map[string]any, param string) error {
	return h.HandleParameter(ParameterContext{
		Document:    doc,
		Parameter:   param,
		RoutedModel: "",
	})
}

// ============================================================
// StripParameter
// ============================================================

func TestStripParameter(t *testing.T) {
	doc := map[string]any{"x": "y", "keep": 1}
	if err := run(StripParameter{}, doc, "x"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["x"]; has {
		t.Fatal("x must be stripped")
	}
	if _, has := doc["keep"]; !has {
		t.Fatal("other fields must remain")
	}
}

func TestStripParameter_NoopWhenAbsent(t *testing.T) {
	doc := map[string]any{"keep": 1}
	if err := run(StripParameter{}, doc, "x"); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// ConditionalStripParameter
// ============================================================

func TestConditionalStripParameter_PredicateTrue(t *testing.T) {
	doc := map[string]any{"min_tokens": 10, "stop_token_ids": []any{42}}
	h := ConditionalStripParameter{
		Predicate: func(ctx ParameterContext) bool {
			_, ok := ctx.Document["stop_token_ids"]
			return ok
		},
	}
	if err := run(h, doc, "min_tokens"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["min_tokens"]; has {
		t.Fatal("min_tokens must be stripped when stop_token_ids present")
	}
}

func TestConditionalStripParameter_PredicateFalse(t *testing.T) {
	doc := map[string]any{"min_tokens": 10}
	h := ConditionalStripParameter{
		Predicate: func(ctx ParameterContext) bool {
			_, ok := ctx.Document["stop_token_ids"]
			return ok
		},
	}
	if err := run(h, doc, "min_tokens"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["min_tokens"]; !has {
		t.Fatal("min_tokens must remain when predicate is false")
	}
}

func TestConditionalStripParameter_NilPredicateNoop(t *testing.T) {
	doc := map[string]any{"x": 1}
	if err := run(ConditionalStripParameter{}, doc, "x"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["x"]; !has {
		t.Fatal("nil predicate is a no-op")
	}
}

// ============================================================
// SanitizeStringListParameter
// ============================================================

func TestSanitizeStringListParameter_Filters(t *testing.T) {
	doc := map[string]any{"bad_words": []any{"keep", "", "  ", "also-keep"}}
	h := SanitizeStringListParameter{
		Keep:             func(s string) bool { return strings.TrimSpace(s) != "" },
		DropFieldIfEmpty: true,
	}
	if err := run(h, doc, "bad_words"); err != nil {
		t.Fatal(err)
	}
	out := doc["bad_words"].([]any)
	if len(out) != 2 || out[0] != "keep" || out[1] != "also-keep" {
		t.Fatalf("want [keep, also-keep], got %v", out)
	}
}

func TestSanitizeStringListParameter_DropsFieldWhenEmpty(t *testing.T) {
	doc := map[string]any{"bad_words": []any{"", "  "}}
	h := SanitizeStringListParameter{
		Keep:             func(s string) bool { return strings.TrimSpace(s) != "" },
		DropFieldIfEmpty: true,
	}
	if err := run(h, doc, "bad_words"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["bad_words"]; has {
		t.Fatal("empty post-filter array must be removed when DropFieldIfEmpty=true")
	}
}

func TestSanitizeStringListParameter_NonStringElementsPreserved(t *testing.T) {
	// Non-strings pass through so later validators can flag them; the filter
	// only acts on strings.
	doc := map[string]any{"x": []any{"a", 42, true, "b"}}
	h := SanitizeStringListParameter{Keep: func(s string) bool { return true }}
	if err := run(h, doc, "x"); err != nil {
		t.Fatal(err)
	}
	out := doc["x"].([]any)
	if len(out) != 4 {
		t.Fatalf("want 4 elements, got %d", len(out))
	}
}

func TestSanitizeStringListParameter_AbsentFieldNoop(t *testing.T) {
	doc := map[string]any{}
	if err := run(SanitizeStringListParameter{}, doc, "x"); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// SanitizeFloatParameter
// ============================================================

func TestSanitizeFloatParameter_Clamps(t *testing.T) {
	doc := map[string]any{"t": 5.0}
	h := SanitizeFloatParameter{Min: testutil.FloatPtr(0), Max: testutil.FloatPtr(2.0)}
	if err := run(h, doc, "t"); err != nil {
		t.Fatal(err)
	}
	if doc["t"].(float64) != 2.0 {
		t.Fatalf("want clamped to 2.0, got %v", doc["t"])
	}
}

func TestSanitizeFloatParameter_StripsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		doc := map[string]any{"t": v}
		h := SanitizeFloatParameter{StripNonFinite: true}
		if err := run(h, doc, "t"); err != nil {
			t.Fatal(err)
		}
		if _, has := doc["t"]; has {
			t.Fatalf("non-finite %v must be stripped", v)
		}
	}
}

func TestSanitizeFloatParameter_NonNumericStripped(t *testing.T) {
	doc := map[string]any{"t": "not-a-number"}
	if err := run(SanitizeFloatParameter{}, doc, "t"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["t"]; has {
		t.Fatal("non-numeric must be stripped")
	}
}

func TestSanitizeFloatParameter_StringNumericCoerced(t *testing.T) {
	// Some SDKs serialize numbers as strings for high-precision fields.
	doc := map[string]any{"t": "1.5"}
	if err := run(SanitizeFloatParameter{}, doc, "t"); err != nil {
		t.Fatal(err)
	}
	if doc["t"].(float64) != 1.5 {
		t.Fatalf("want 1.5, got %v", doc["t"])
	}
}

// ============================================================
// SanitizeFloatMapParameter
// ============================================================

func TestSanitizeFloatMapParameter_DropsOutOfRange(t *testing.T) {
	doc := map[string]any{"logit_bias": map[string]any{
		"a": 50.0,   // out of [-100, 100]? No, in range
		"b": 150.0,  // > 100 → drop
		"c": -200.0, // < -100 → drop
		"d": 0.0,
	}}
	h := SanitizeFloatMapParameter{Min: testutil.FloatPtr(-100), Max: testutil.FloatPtr(100)}
	if err := run(h, doc, "logit_bias"); err != nil {
		t.Fatal(err)
	}
	out := doc["logit_bias"].(map[string]any)
	if _, has := out["b"]; has {
		t.Fatal("b must be dropped")
	}
	if _, has := out["c"]; has {
		t.Fatal("c must be dropped")
	}
	if len(out) != 2 {
		t.Fatalf("want 2 surviving entries, got %d", len(out))
	}
}

func TestSanitizeFloatMapParameter_DropsNonFinite(t *testing.T) {
	doc := map[string]any{"m": map[string]any{
		"a": 1.0,
		"b": math.NaN(),
		"c": math.Inf(1),
	}}
	h := SanitizeFloatMapParameter{StripNonFinite: true}
	if err := run(h, doc, "m"); err != nil {
		t.Fatal(err)
	}
	out := doc["m"].(map[string]any)
	if len(out) != 1 {
		t.Fatalf("want 1 surviving, got %d", len(out))
	}
}

func TestSanitizeFloatMapParameter_RejectsOversizeMap(t *testing.T) {
	doc := map[string]any{"m": map[string]any{}}
	for i := 0; i < 5; i++ {
		doc["m"].(map[string]any)[string(rune('a'+i))] = 1.0
	}
	h := SanitizeFloatMapParameter{MaxEntries: 3}
	err := run(h, doc, "m")
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("want 'exceeds limit', got %v", err)
	}
}

func TestSanitizeFloatMapParameter_DropFieldIfEmpty(t *testing.T) {
	doc := map[string]any{"m": map[string]any{"a": math.NaN(), "b": math.Inf(1)}}
	h := SanitizeFloatMapParameter{StripNonFinite: true, DropFieldIfEmpty: true}
	if err := run(h, doc, "m"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["m"]; has {
		t.Fatal("post-filter empty map must be removed")
	}
}

// ============================================================
// ForceLiteralParameter
// ============================================================

func TestForceLiteralParameter_Overwrites(t *testing.T) {
	doc := map[string]any{"logprobs": false}
	if err := run(ForceLiteralParameter{Value: true}, doc, "logprobs"); err != nil {
		t.Fatal(err)
	}
	if doc["logprobs"] != true {
		t.Fatalf("want true, got %v", doc["logprobs"])
	}
}

func TestForceLiteralParameter_CreatesWhenAbsent(t *testing.T) {
	doc := map[string]any{}
	if err := run(ForceLiteralParameter{Value: 5}, doc, "n"); err != nil {
		t.Fatal(err)
	}
	if doc["n"] != 5 {
		t.Fatalf("want 5, got %v", doc["n"])
	}
}

func TestForceLiteralParameter_OverwriteOnlySkipsAbsent(t *testing.T) {
	doc := map[string]any{}
	h := ForceLiteralParameter{Value: 0.0, OverwriteOnly: true}
	if err := run(h, doc, "frequency_penalty"); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["frequency_penalty"]; has {
		t.Fatal("OverwriteOnly must not create absent field")
	}
}

func TestForceLiteralParameter_OverwriteOnlyTouchesPresent(t *testing.T) {
	doc := map[string]any{"frequency_penalty": 0.5}
	h := ForceLiteralParameter{Value: 0.0, OverwriteOnly: true}
	if err := run(h, doc, "frequency_penalty"); err != nil {
		t.Fatal(err)
	}
	if doc["frequency_penalty"] != 0.0 {
		t.Fatalf("want 0.0, got %v", doc["frequency_penalty"])
	}
}

// ============================================================
// CapUintParameter
// ============================================================

func TestCapUintParameter(t *testing.T) {
	cases := []struct {
		name string
		in   any
		max  uint64
		want any
	}{
		{"caps-when-over", float64(100), 10, uint64(10)},
		{"leaves-when-under", float64(5), 10, float64(5)},
		{"leaves-when-equal", float64(10), 10, float64(10)},
		{"non-numeric-noop", "x", 10, "x"},
		{"negative-noop", float64(-1), 10, float64(-1)}, // not coerced → not capped
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{"n": tc.in}
			if err := run(CapUintParameter{Max: tc.max}, doc, "n"); err != nil {
				t.Fatal(err)
			}
			if doc["n"] != tc.want {
				t.Fatalf("want %v, got %v", tc.want, doc["n"])
			}
		})
	}
}

// ============================================================
// ClampUintToFieldParameter
// ============================================================

func TestClampUintToFieldParameter(t *testing.T) {
	t.Run("clamps-to-other-field", func(t *testing.T) {
		doc := map[string]any{"min_tokens": float64(1000), "max_tokens": float64(100)}
		if err := run(ClampUintToFieldParameter{MaxField: "max_tokens"}, doc, "min_tokens"); err != nil {
			t.Fatal(err)
		}
		if doc["min_tokens"] != uint64(100) {
			t.Fatalf("want clamped to 100, got %v", doc["min_tokens"])
		}
	})
	t.Run("max-field-absent-noop", func(t *testing.T) {
		doc := map[string]any{"min_tokens": float64(1000)}
		if err := run(ClampUintToFieldParameter{MaxField: "max_tokens"}, doc, "min_tokens"); err != nil {
			t.Fatal(err)
		}
		if doc["min_tokens"] != float64(1000) {
			t.Fatal("must not clamp when max field is absent")
		}
	})
	t.Run("max-field-zero-noop", func(t *testing.T) {
		// max_tokens=0 means "no max set" — don't clamp to 0.
		doc := map[string]any{"min_tokens": float64(1000), "max_tokens": float64(0)}
		if err := run(ClampUintToFieldParameter{MaxField: "max_tokens"}, doc, "min_tokens"); err != nil {
			t.Fatal(err)
		}
		if doc["min_tokens"] != float64(1000) {
			t.Fatal("max=0 must not clamp")
		}
	})
}

// ============================================================
// ValidateUintParameter
// ============================================================

func TestValidateUintParameter(t *testing.T) {
	cases := []struct {
		name      string
		in        any
		errSubstr string
	}{
		{"valid-int", float64(42), ""},
		{"absent", nil, ""},
		{"negative-rejected", float64(-1), "must be a non-negative integer"},
		{"float-rejected", float64(1.5), "must be a non-negative integer"},
		{"string-rejected", "42", "must be a non-negative integer"},
		{"bool-rejected", true, "must be a non-negative integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{}
			if tc.name != "absent" {
				doc["seed"] = tc.in
			}
			err := run(ValidateUintParameter{}, doc, "seed")
			if tc.errSubstr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errSubstr)
			}
			if !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("want %q, got %q", tc.errSubstr, err.Error())
			}
		})
	}
}

// ============================================================
// LengthCapListParameter
// ============================================================

func TestLengthCapListParameter_CapsArray(t *testing.T) {
	doc := map[string]any{"stop": []any{"a", "b", "c"}}
	err := run(LengthCapListParameter{MaxEntries: 2}, doc, "stop")
	if err == nil || !strings.Contains(err.Error(), "array length") {
		t.Fatalf("want 'array length' error, got %v", err)
	}
}

func TestLengthCapListParameter_CapsEntryLen(t *testing.T) {
	doc := map[string]any{"stop": []any{"short", strings.Repeat("x", 100)}}
	err := run(LengthCapListParameter{MaxEntryLen: 10}, doc, "stop")
	if err == nil || !strings.Contains(err.Error(), "string length") {
		t.Fatalf("want 'string length' error, got %v", err)
	}
}

func TestLengthCapListParameter_NonStringEntriesSkipped(t *testing.T) {
	// stop_token_ids is an int array — string cap doesn't apply.
	doc := map[string]any{"stop_token_ids": []any{1, 2, 3, 4}}
	if err := run(LengthCapListParameter{MaxEntries: 0, MaxEntryLen: 5}, doc, "stop_token_ids"); err != nil {
		t.Fatal(err)
	}
}

func TestLengthCapListParameter_WithinCapOK(t *testing.T) {
	doc := map[string]any{"stop": []any{"a", "b"}}
	if err := run(LengthCapListParameter{MaxEntries: 5, MaxEntryLen: 10}, doc, "stop"); err != nil {
		t.Fatal(err)
	}
}

func TestLengthCapListParameter_AbsentFieldNoop(t *testing.T) {
	doc := map[string]any{}
	if err := run(LengthCapListParameter{MaxEntries: 1}, doc, "stop"); err != nil {
		t.Fatal(err)
	}
}
