package otelutil

import (
	"strings"
	"testing"
)

func TestParseHeaders(t *testing.T) {
	type malformedCall struct {
		reason, key string
	}
	cases := []struct {
		name      string
		raw       string
		want      map[string]string
		malformed []malformedCall
	}{
		{name: "empty", raw: "", want: nil},
		{name: "whitespace", raw: "   ", want: nil},
		{name: "single", raw: "k=v", want: map[string]string{"k": "v"}},
		{name: "multi_with_spaces", raw: " a=1 ,  b=2 ", want: map[string]string{"a": "1", "b": "2"}},
		{name: "skip_blank_pair", raw: "a=1,,b=2", want: map[string]string{"a": "1", "b": "2"}},
		{
			name: "malformed_no_eq",
			raw:  "a=1,broken,b=2",
			want: map[string]string{"a": "1", "b": "2"},
			malformed: []malformedCall{
				{reason: MalformedMissingSeparator},
			},
		},
		{
			name: "malformed_empty_key",
			raw:  "=v,a=1",
			want: map[string]string{"a": "1"},
			malformed: []malformedCall{
				{reason: MalformedEmptyKey},
			},
		},
		{
			name: "malformed_empty_value",
			raw:  "authorization=,a=1",
			want: map[string]string{"a": "1"},
			malformed: []malformedCall{
				{reason: MalformedEmptyValue, key: "authorization"},
			},
		},
		{
			name: "all_malformed_returns_nil",
			raw:  "broken,=v",
			want: nil,
			malformed: []malformedCall{
				{reason: MalformedMissingSeparator},
				{reason: MalformedEmptyKey},
			},
		},
		{
			name: "colon_typo_does_not_report_secret",
			raw:  "authorization:Bearer super-secret-token",
			want: nil,
			malformed: []malformedCall{
				{reason: MalformedMissingSeparator},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []malformedCall
			out := ParseHeaders(tc.raw, func(reason, key string) {
				got = append(got, malformedCall{reason: reason, key: key})
			})
			if len(out) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", out, tc.want)
			}
			for k, v := range tc.want {
				if out[k] != v {
					t.Errorf("key %q: got %q want %q", k, out[k], v)
				}
			}
			if len(got) != len(tc.malformed) {
				t.Fatalf("malformed callbacks: got %v want %v", got, tc.malformed)
			}
			for i, want := range tc.malformed {
				if got[i] != want {
					t.Errorf("malformed[%d]: got %+v want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestParseHeaders_OnMalformedNeverReceivesSecrets(t *testing.T) {
	secret := "super-secret-token"
	raw := "authorization:Bearer " + secret + ",=Bearer " + secret + ",x="
	ParseHeaders(raw, func(reason, key string) {
		if strings.Contains(reason, secret) || strings.Contains(key, secret) {
			t.Fatalf("callback leaked secret: reason=%q key=%q", reason, key)
		}
		if strings.Contains(reason, "Bearer") || strings.Contains(key, "Bearer") {
			t.Fatalf("callback leaked bearer material: reason=%q key=%q", reason, key)
		}
	})
}
