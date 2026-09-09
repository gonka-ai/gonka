package messagevalidators

import (
	"strings"
	"testing"
)

func TestRequiredNonEmptyString(t *testing.T) {
	cases := []struct {
		name      string
		values    map[string]any
		field     string
		want      string
		errSubstr string
	}{
		{"happy", map[string]any{"x": "hello"}, "x", "hello", ""},
		{"trimmed-keeps-original", map[string]any{"x": "  hello  "}, "x", "  hello  ", ""},
		{"missing", map[string]any{}, "x", "", "is required"},
		{"explicit-nil", map[string]any{"x": nil}, "x", "", "is required"},
		{"non-string", map[string]any{"x": 42}, "x", "", "must be a string"},
		{"bool-not-string", map[string]any{"x": true}, "x", "", "must be a string"},
		{"empty-string", map[string]any{"x": ""}, "x", "", "must not be empty"},
		{"whitespace-only", map[string]any{"x": "   \t\n"}, "x", "", "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RequiredNonEmptyString(tc.values, tc.field)
			if tc.errSubstr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				if got != tc.want {
					t.Fatalf("want %q, got %q", tc.want, got)
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

func TestOptionalStringField(t *testing.T) {
	cases := []struct {
		name      string
		values    map[string]any
		field     string
		errSubstr string
	}{
		{"missing-ok", map[string]any{}, "x", ""},
		{"explicit-nil-ok", map[string]any{"x": nil}, "x", ""},
		{"empty-string-ok", map[string]any{"x": ""}, "x", ""},
		{"valid-string-ok", map[string]any{"x": "hello"}, "x", ""},
		{"non-string-rejected", map[string]any{"x": 42}, "x", "must be a string"},
		{"object-rejected", map[string]any{"x": map[string]any{}}, "x", "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := OptionalStringField(tc.values, tc.field)
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

func TestEnsureFieldsAbsent(t *testing.T) {
	t.Run("all-absent-ok", func(t *testing.T) {
		err := EnsureFieldsAbsent(map[string]any{"role": "user"}, "tool_calls", "tool_call_id")
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
	})
	t.Run("present-rejected", func(t *testing.T) {
		err := EnsureFieldsAbsent(map[string]any{"role": "user", "tool_calls": []any{}}, "tool_calls")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "tool_calls is not allowed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("first-violation-wins", func(t *testing.T) {
		// Multiple violations: caller sees the first listed disallowed field.
		err := EnsureFieldsAbsent(map[string]any{"a": 1, "b": 2}, "a", "b")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "a is not allowed") {
			t.Fatalf("want first-violation 'a', got %v", err)
		}
	})
	t.Run("nil-value-still-present", func(t *testing.T) {
		// An explicit null in the request still counts as "present" — clients that
		// emit nil placeholders must not bypass the role policy.
		err := EnsureFieldsAbsent(map[string]any{"tool_calls": nil}, "tool_calls")
		if err == nil {
			t.Fatal("want error, got nil")
		}
	})
	t.Run("variadic-empty-ok", func(t *testing.T) {
		err := EnsureFieldsAbsent(map[string]any{"a": 1}, []string{}...)
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}
	})
}
