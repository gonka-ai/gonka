package vo_test

import (
	"errors"
	"strings"
	"testing"

	"trainshard/internal/domain/shared/vo"
)

func TestParseEndpointTakesAnHTTPBaseAndNothingElse(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		valid bool
	}{
		{name: "empty is no endpoint", raw: "", valid: true},
		{name: "https host", raw: "https://gpu1.example.com", valid: true},
		{name: "http host and port", raw: "http://10.0.0.5:9700", valid: true},
		{name: "path prefix a proxy routes on", raw: "https://example.com/trainshard-node2", valid: true},
		{name: "trailing slash", raw: "https://example.com/", valid: false},
		{name: "no scheme", raw: "example.com:9700", valid: false},
		{name: "other scheme", raw: "wss://example.com", valid: false},
		{name: "query", raw: "https://example.com?x=1", valid: false},
		{name: "fragment", raw: "https://example.com#x", valid: false},
		{name: "bare query mark", raw: "https://example.com?", valid: false},
		{name: "bare fragment mark", raw: "https://example.com#", valid: false},
		{name: "credentials", raw: "https://u:p@example.com", valid: false},
		{name: "no host", raw: "https://", valid: false},
		{name: "port zero", raw: "http://10.0.0.5:0", valid: false},
		{name: "colon without a port", raw: "http://10.0.0.5:", valid: false},
		{name: "colon without a port before a path", raw: "http://10.0.0.5:/t", valid: false},
		{name: "port out of range", raw: "http://10.0.0.5:70000", valid: false},
		{name: "too long", raw: "https://" + strings.Repeat("a", 250) + ".com", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vo.ParseEndpoint(tc.raw)
			if tc.valid {
				if err != nil || string(got) != tc.raw {
					t.Fatalf("got %q, %v; want %q accepted", got, err, tc.raw)
				}
				return
			}
			if !errors.Is(err, vo.ErrEndpointInvalid) {
				t.Fatalf("got %q, %v; want refused", got, err)
			}
		})
	}
}
