package paramvalidators

import (
	"strings"
	"testing"
)

// ============================================================
// RejectNumberParameter
// ============================================================

func TestRejectNumberParameter(t *testing.T) {
	positive := RejectNumberParameter{Allow: func(v float64) bool { return v > 0 }, Message: "must be greater than 0"}

	// Absent and non-numeric values pass through — dedicated shape validators own those.
	if err := run(positive, map[string]any{}, "top_p"); err != nil {
		t.Fatalf("absent field must pass through, got %v", err)
	}
	if err := run(positive, map[string]any{"top_p": "not-a-number"}, "top_p"); err != nil {
		t.Fatalf("non-numeric must pass through, got %v", err)
	}
	if err := run(positive, map[string]any{"top_p": 0.5}, "top_p"); err != nil {
		t.Fatalf("allowed value must pass, got %v", err)
	}

	err := run(positive, map[string]any{"top_p": 0}, "top_p")
	if err == nil || !strings.Contains(err.Error(), "top_p") || !strings.Contains(err.Error(), "greater than 0") {
		t.Fatalf("non-positive must be rejected with a field-named error, got %v", err)
	}
}

func TestRejectNumberParameter_TopKPredicate(t *testing.T) {
	topK := RejectNumberParameter{Allow: func(v float64) bool { return v == -1 || v >= 1 }, Message: "must be -1 or a positive integer"}
	for _, good := range []any{-1, 1, 50} {
		if err := run(topK, map[string]any{"top_k": good}, "top_k"); err != nil {
			t.Fatalf("top_k=%v must pass, got %v", good, err)
		}
	}
	for _, bad := range []any{0, -2} {
		if err := run(topK, map[string]any{"top_k": bad}, "top_k"); err == nil {
			t.Fatalf("top_k=%v must be rejected", bad)
		}
	}
}

// ============================================================
// ValidateScalarParameter
// ============================================================

func TestValidateScalarParameter(t *testing.T) {
	mustBeBool := ValidateScalarParameter{Valid: IsJSONBool, Message: "must be a boolean"}

	// Absent or explicit null passes through.
	if err := run(mustBeBool, map[string]any{}, "stream"); err != nil {
		t.Fatalf("absent must pass, got %v", err)
	}
	if err := run(mustBeBool, map[string]any{"stream": nil}, "stream"); err != nil {
		t.Fatalf("null must pass, got %v", err)
	}
	if err := run(mustBeBool, map[string]any{"stream": true}, "stream"); err != nil {
		t.Fatalf("bool must pass, got %v", err)
	}
	if err := run(mustBeBool, map[string]any{"stream": "yes"}, "stream"); err == nil {
		t.Fatal("non-bool must be rejected")
	}
}

// ============================================================
// ValidateListElementsParameter
// ============================================================

func TestValidateListElementsParameter(t *testing.T) {
	elementsMustBeString := ValidateListElementsParameter{Valid: IsJSONString, Message: "must be a string"}

	// Absent or non-array passes through.
	if err := run(elementsMustBeString, map[string]any{}, "stop"); err != nil {
		t.Fatalf("absent must pass, got %v", err)
	}
	if err := run(elementsMustBeString, map[string]any{"stop": "single"}, "stop"); err != nil {
		t.Fatalf("non-array must pass, got %v", err)
	}
	if err := run(elementsMustBeString, map[string]any{"stop": []any{"a", "b"}}, "stop"); err != nil {
		t.Fatalf("all-string array must pass, got %v", err)
	}

	err := run(elementsMustBeString, map[string]any{"stop": []any{"a", 5, "c"}}, "stop")
	if err == nil || !strings.Contains(err.Error(), "stop[1]") {
		t.Fatalf("bad element must be rejected with its index, got %v", err)
	}
}

func TestJSONTypePredicates(t *testing.T) {
	if !IsJSONBool(true) || IsJSONBool("x") {
		t.Fatal("IsJSONBool")
	}
	if !IsJSONString("x") || IsJSONString(1) {
		t.Fatal("IsJSONString")
	}
	if !IsJSONUint(5) || IsJSONUint(-1) || IsJSONUint(3.5) {
		t.Fatal("IsJSONUint")
	}
}
