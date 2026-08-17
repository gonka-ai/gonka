package messagevalidators

import (
	"strings"
	"testing"
)

func TestValidateNonEmptyContent(t *testing.T) {
	cases := []struct {
		name      string
		content   any
		errSubstr string
	}{
		{"string-ok", "hello", ""},
		{"string-with-think-block", "<think>step</think> answer", ""},
		{"string-empty", "", "must not be empty"},
		{"string-whitespace", "  \t\n  ", "must not be empty"},
		{"array-of-text-parts-ok", []any{map[string]any{"type": "text", "text": "hello"}}, ""},
		{"array-empty", []any{}, "must not be empty"},
		{"array-element-not-object", []any{"not-an-object"}, "[0] must be an object"},
		{"array-part-missing-type", []any{map[string]any{"text": "no type"}}, "[0].type"},
		{"array-part-wrong-type", []any{map[string]any{"type": "image_url", "text": "x"}}, "unsupported value"},
		{"array-part-empty-text", []any{map[string]any{"type": "text", "text": " "}}, "[0].text"},
		{"number-not-supported", 42, "must be a string or an array"},
		{"object-not-supported", map[string]any{}, "must be a string or an array"},
		{"nil-not-supported", nil, "must be a string or an array"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNonEmptyContent(tc.content)
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
				t.Fatalf("want error containing %q, got %q", tc.errSubstr, err.Error())
			}
		})
	}
}

func TestValidateRequiredContentField(t *testing.T) {
	t.Run("missing-rejected", func(t *testing.T) {
		err := ValidateRequiredContentField(map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "is required") {
			t.Fatalf("want 'is required', got %v", err)
		}
	})
	t.Run("nil-rejected", func(t *testing.T) {
		err := ValidateRequiredContentField(map[string]any{"content": nil})
		if err == nil || !strings.Contains(err.Error(), "is required") {
			t.Fatalf("want 'is required', got %v", err)
		}
	})
	t.Run("string-ok", func(t *testing.T) {
		err := ValidateRequiredContentField(map[string]any{"content": "hi"})
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
	})
}

func TestValidateAssistantContentField(t *testing.T) {
	t.Run("missing-with-canBeEmpty-ok", func(t *testing.T) {
		// Assistant turn with tool_calls/function_call can omit content.
		err := ValidateAssistantContentField(map[string]any{}, true)
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
	})
	t.Run("missing-without-canBeEmpty-rejected", func(t *testing.T) {
		err := ValidateAssistantContentField(map[string]any{}, false)
		if err == nil || !strings.Contains(err.Error(), "tool_calls or function_call") {
			t.Fatalf("want hint about tool_calls/function_call, got %v", err)
		}
	})
	t.Run("nil-with-canBeEmpty-ok", func(t *testing.T) {
		err := ValidateAssistantContentField(map[string]any{"content": nil}, true)
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
	})
	t.Run("non-empty-content-still-validated", func(t *testing.T) {
		// Even when canBeEmpty=true, present content must not be empty/wrong shape.
		err := ValidateAssistantContentField(map[string]any{"content": ""}, true)
		if err == nil {
			t.Fatal("empty string content must still be rejected")
		}
	})
}

func TestRequiredTextContentPart(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		text, err := RequiredTextContentPart(map[string]any{"type": "text", "text": "hello"}, 0)
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
		if text != "hello" {
			t.Fatalf("want 'hello', got %q", text)
		}
	})
	t.Run("missing-type", func(t *testing.T) {
		_, err := RequiredTextContentPart(map[string]any{"text": "hi"}, 2)
		if err == nil || !strings.Contains(err.Error(), "[2].type") {
			t.Fatalf("want '[2].type', got %v", err)
		}
	})
	t.Run("wrong-type", func(t *testing.T) {
		_, err := RequiredTextContentPart(map[string]any{"type": "image_url", "text": "x"}, 3)
		if err == nil || !strings.Contains(err.Error(), "unsupported value") {
			t.Fatalf("want 'unsupported value', got %v", err)
		}
	})
	t.Run("missing-text", func(t *testing.T) {
		_, err := RequiredTextContentPart(map[string]any{"type": "text"}, 0)
		if err == nil || !strings.Contains(err.Error(), "[0].text") {
			t.Fatalf("want '[0].text', got %v", err)
		}
	})
}

func TestCombineTextContentParts(t *testing.T) {
	t.Run("single-part", func(t *testing.T) {
		got, err := CombineTextContentParts([]any{map[string]any{"type": "text", "text": "hello"}})
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
		if got != "hello" {
			t.Fatalf("want 'hello', got %q", got)
		}
	})
	t.Run("multiple-parts-newline-join", func(t *testing.T) {
		got, err := CombineTextContentParts([]any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "text", "text": "second"},
		})
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
		if got != "first\nsecond" {
			t.Fatalf("want 'first\\nsecond', got %q", got)
		}
	})
	t.Run("empty-array-returns-empty-string", func(t *testing.T) {
		got, err := CombineTextContentParts([]any{})
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
		if got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})
	t.Run("non-object-element", func(t *testing.T) {
		_, err := CombineTextContentParts([]any{"plain-string"})
		if err == nil || !strings.Contains(err.Error(), "[0] must be an object") {
			t.Fatalf("want '[0] must be an object', got %v", err)
		}
	})
	t.Run("propagates-part-error", func(t *testing.T) {
		_, err := CombineTextContentParts([]any{map[string]any{"type": "image_url", "text": "x"}})
		if err == nil || !strings.Contains(err.Error(), "unsupported value") {
			t.Fatalf("want 'unsupported value', got %v", err)
		}
	})
	t.Run("preserves-think-block-in-text", func(t *testing.T) {
		// MiniMax-M2.7 round-trips <think>...</think> verbatim in content.
		got, err := CombineTextContentParts([]any{
			map[string]any{"type": "text", "text": "<think>reasoning</think>"},
			map[string]any{"type": "text", "text": "answer"},
		})
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
		if got != "<think>reasoning</think>\nanswer" {
			t.Fatalf("think block must round-trip, got %q", got)
		}
	})
}

func TestIsEmptyContent(t *testing.T) {
	cases := []struct {
		name    string
		content any
		want    bool
	}{
		{"empty-string", "", true},
		{"whitespace-string", "   ", true},
		{"non-empty-string", "x", false},
		{"empty-array", []any{}, true},
		{"non-empty-array", []any{1}, false},
		{"nil-not-empty", nil, false}, // nil is "missing", not "empty"
		{"int-not-empty", 42, false},
		{"object-not-empty", map[string]any{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEmptyContent(tc.content); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestIsAssistantTurnEmpty(t *testing.T) {
	t.Run("no-fields-empty", func(t *testing.T) {
		if !IsAssistantTurnEmpty(map[string]any{}) {
			t.Fatal("empty turn must be empty")
		}
	})
	t.Run("content-only-non-empty", func(t *testing.T) {
		if IsAssistantTurnEmpty(map[string]any{"content": "hi"}) {
			t.Fatal("turn with content must not be empty")
		}
	})
	t.Run("tool-calls-non-empty", func(t *testing.T) {
		msg := map[string]any{"tool_calls": []any{map[string]any{"id": "x"}}}
		if IsAssistantTurnEmpty(msg) {
			t.Fatal("turn with tool_calls must not be empty")
		}
	})
	t.Run("empty-tool-calls-array-still-empty", func(t *testing.T) {
		// Empty tool_calls = informationless placeholder.
		msg := map[string]any{"tool_calls": []any{}}
		if !IsAssistantTurnEmpty(msg) {
			t.Fatal("empty tool_calls slot is informationless")
		}
	})
	t.Run("nil-tool-calls-treated-as-absent", func(t *testing.T) {
		msg := map[string]any{"tool_calls": nil}
		if !IsAssistantTurnEmpty(msg) {
			t.Fatal("nil tool_calls is absent")
		}
	})
	t.Run("function-call-non-empty", func(t *testing.T) {
		msg := map[string]any{"function_call": map[string]any{"name": "fn"}}
		if IsAssistantTurnEmpty(msg) {
			t.Fatal("turn with function_call must not be empty")
		}
	})
	t.Run("empty-function-call-object-empty", func(t *testing.T) {
		msg := map[string]any{"function_call": map[string]any{}}
		if !IsAssistantTurnEmpty(msg) {
			t.Fatal("empty function_call object is informationless")
		}
	})
	t.Run("nil-content-empty", func(t *testing.T) {
		if !IsAssistantTurnEmpty(map[string]any{"content": nil}) {
			t.Fatal("nil content is empty")
		}
	})
	t.Run("empty-content-array-empty", func(t *testing.T) {
		if !IsAssistantTurnEmpty(map[string]any{"content": []any{}}) {
			t.Fatal("empty content array is empty")
		}
	})
}
