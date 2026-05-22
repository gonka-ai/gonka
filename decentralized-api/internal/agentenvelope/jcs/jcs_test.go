package jcs

import "testing"

func canon(t *testing.T, in string) string {
	t.Helper()
	out, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize(%q) error: %v", in, err)
	}
	return string(out)
}

func TestCanonicalize_KeySortingUTF16(t *testing.T) {
	// Keys sort by UTF-16 code unit: 'Z'(0x5A) < 'a'(0x61) < 'b'(0x62) < U+00E9.
	got := canon(t, "{\"b\":\"1\",\"é\":\"4\",\"a\":\"2\",\"Z\":\"3\"}")
	want := "{\"Z\":\"3\",\"a\":\"2\",\"b\":\"1\",\"é\":\"4\"}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_StringEscapes(t *testing.T) {
	// The seven short escapes, plus \u00XX lowercase hex for other C0 controls.
	got := canon(t, `{"k":"a\nb\tcd\"e\\f"}`)
	want := `{"k":"a\nb\tcd\"e\\f"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_LineSeparatorNotEscaped(t *testing.T) {
	// RFC 8785 emits U+2028 literally, unlike Go encoding/json.
	got := canon(t, "{\"k\":\" \"}")
	want := "{\"k\":\" \"}"
	if got != want {
		t.Errorf("U+2028 should be literal: got %q, want %q", got, want)
	}
}

func TestCanonicalize_Nested(t *testing.T) {
	got := canon(t, `{"outer":{"y":"2","x":"1"},"arr":["c","a","b"]}`)
	want := `{"arr":["c","a","b"],"outer":{"x":"1","y":"2"}}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_Numbers(t *testing.T) {
	cases := map[string]string{
		`{"n":1e2}`:  `{"n":100}`,
		`{"n":1.0}`:  `{"n":1}`,
		`{"n":-0}`:   `{"n":0}`,
		`{"n":0}`:    `{"n":0}`,
		`{"n":-123}`: `{"n":-123}`,
	}
	for in, want := range cases {
		if got := canon(t, in); got != want {
			t.Errorf("Canonicalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalize_LiteralsAndArrays(t *testing.T) {
	got := canon(t, `{"a":true,"b":false,"c":null,"d":[]}`)
	want := `{"a":true,"b":false,"c":null,"d":[]}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_Idempotent(t *testing.T) {
	in := `{"b":"1","a":{"z":"9","y":"8"},"c":["x","w"]}`
	once := canon(t, in)
	twice := canon(t, once)
	if once != twice {
		t.Errorf("not idempotent: once=%q twice=%q", once, twice)
	}
}

func TestCanonicalize_RejectsTrailingContent(t *testing.T) {
	if _, err := Canonicalize([]byte(`{"a":"1"} {"b":"2"}`)); err == nil {
		t.Error("expected error for trailing content, got nil")
	}
}

func TestCanonicalize_RejectsInvalidJSON(t *testing.T) {
	if _, err := Canonicalize([]byte(`{"a":}`)); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestCanonicalize_RejectsRawControlChar(t *testing.T) {
	// A literal unescaped control byte inside a string is invalid JSON.
	if _, err := Canonicalize([]byte("{\"k\":\"a\x01b\"}")); err == nil {
		t.Error("expected error for raw control char in string, got nil")
	}
}
