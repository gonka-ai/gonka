package messagevalidators

import (
	"strings"
	"testing"
)

func TestValidateToolCallsField_Absent(t *testing.T) {
	msg := map[string]any{}
	ids, has, err := ValidateToolCallsField(msg)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if has {
		t.Fatal("hasField must be false when field absent")
	}
	if ids != nil {
		t.Fatalf("want nil ids, got %v", ids)
	}
}

func TestValidateToolCallsField_NullTreatedAsAbsent(t *testing.T) {
	// LangChain serializes empty slots as `null`; gateway silently drops it.
	msg := map[string]any{"tool_calls": nil}
	ids, has, err := ValidateToolCallsField(msg)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if has {
		t.Fatal("hasField must be false when value is null")
	}
	if _, stillThere := msg["tool_calls"]; stillThere {
		t.Fatal("null tool_calls must be deleted from message")
	}
	if ids != nil {
		t.Fatalf("want nil ids, got %v", ids)
	}
}

func TestValidateToolCallsField_Happy(t *testing.T) {
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{
				"id":       "call_1",
				"type":     "function",
				"function": map[string]any{"name": "fn", "arguments": "{}"},
			},
			map[string]any{
				"id":       "call_2",
				"type":     "function",
				"function": map[string]any{"name": "fn", "arguments": ""},
			},
		},
	}
	ids, has, err := ValidateToolCallsField(msg)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !has {
		t.Fatal("hasField must be true")
	}
	if len(ids) != 2 || ids[0] != "call_1" || ids[1] != "call_2" {
		t.Fatalf("want [call_1, call_2], got %v", ids)
	}
}

func TestValidateToolCallsField_Errors(t *testing.T) {
	cases := []struct {
		name      string
		msg       map[string]any
		errSubstr string
	}{
		{
			"not-array",
			map[string]any{"tool_calls": "not-array"},
			"tool_calls must be an array",
		},
		{
			"empty-array",
			map[string]any{"tool_calls": []any{}},
			"tool_calls must not be empty",
		},
		{
			"element-not-object",
			map[string]any{"tool_calls": []any{"x"}},
			"tool_calls[0] must be an object",
		},
		{
			"missing-id",
			map[string]any{"tool_calls": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "fn"}},
			}},
			"tool_calls[0].id",
		},
		{
			"duplicate-id",
			map[string]any{"tool_calls": []any{
				map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "fn"}},
				map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "fn"}},
			}},
			"tool_calls[1].id is duplicated",
		},
		{
			"wrong-type",
			map[string]any{"tool_calls": []any{
				map[string]any{"id": "x", "type": "code_interpreter", "function": map[string]any{"name": "fn"}},
			}},
			"tool_calls[0].type",
		},
		{
			"function-not-object",
			map[string]any{"tool_calls": []any{
				map[string]any{"id": "x", "type": "function", "function": "not-object"},
			}},
			"tool_calls[0].function must be an object",
		},
		{
			"function-missing-name",
			map[string]any{"tool_calls": []any{
				map[string]any{"id": "x", "type": "function", "function": map[string]any{}},
			}},
			"tool_calls[0].function.name",
		},
		{
			"function-arguments-not-string",
			map[string]any{"tool_calls": []any{
				map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "fn", "arguments": 42}},
			}},
			"tool_calls[0].function.arguments",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ValidateToolCallsField(tc.msg)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errSubstr)
			}
			if !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("want error containing %q, got %q", tc.errSubstr, err.Error())
			}
		})
	}
}

func TestValidateFunctionCallField_Absent(t *testing.T) {
	has, err := ValidateFunctionCallField(map[string]any{})
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if has {
		t.Fatal("hasField must be false")
	}
}

func TestValidateFunctionCallField_NullTreatedAsAbsent(t *testing.T) {
	msg := map[string]any{"function_call": nil}
	has, err := ValidateFunctionCallField(msg)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if has {
		t.Fatal("hasField must be false")
	}
	if _, stillThere := msg["function_call"]; stillThere {
		t.Fatal("null function_call must be deleted")
	}
}

func TestValidateFunctionCallField_Happy(t *testing.T) {
	has, err := ValidateFunctionCallField(map[string]any{
		"function_call": map[string]any{"name": "fn", "arguments": "{}"},
	})
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !has {
		t.Fatal("hasField must be true")
	}
}

func TestValidateFunctionCallField_Errors(t *testing.T) {
	cases := []struct {
		name      string
		msg       map[string]any
		errSubstr string
	}{
		{"not-object", map[string]any{"function_call": "x"}, "function_call must be an object"},
		{"missing-name", map[string]any{"function_call": map[string]any{}}, "function_call.name"},
		{"args-not-string", map[string]any{"function_call": map[string]any{"name": "fn", "arguments": 42}}, "function_call.arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateFunctionCallField(tc.msg)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errSubstr)
			}
			if !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("want error containing %q, got %q", tc.errSubstr, err.Error())
			}
		})
	}
}
