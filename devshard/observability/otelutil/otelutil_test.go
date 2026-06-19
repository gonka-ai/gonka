package otelutil

import "testing"

func TestParseHeaders(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		want      map[string]string
		malformed []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "whitespace", raw: "   ", want: nil},
		{name: "single", raw: "k=v", want: map[string]string{"k": "v"}},
		{name: "multi_with_spaces", raw: " a=1 ,  b=2 ", want: map[string]string{"a": "1", "b": "2"}},
		{name: "skip_blank_pair", raw: "a=1,,b=2", want: map[string]string{"a": "1", "b": "2"}},
		{name: "malformed_no_eq", raw: "a=1,broken,b=2", want: map[string]string{"a": "1", "b": "2"}, malformed: []string{"broken"}},
		{name: "malformed_empty_key", raw: "=v,a=1", want: map[string]string{"a": "1"}, malformed: []string{"=v"}},
		{name: "all_malformed_returns_nil", raw: "broken,=v", want: nil, malformed: []string{"broken", "=v"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			out := ParseHeaders(tc.raw, func(p string) { got = append(got, p) })
			if len(out) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", out, tc.want)
			}
			for k, v := range tc.want {
				if out[k] != v {
					t.Errorf("key %q: got %q want %q", k, out[k], v)
				}
			}
			if len(got) != len(tc.malformed) {
				t.Errorf("malformed callbacks: got %v want %v", got, tc.malformed)
			}
		})
	}
}
