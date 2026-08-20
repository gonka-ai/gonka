package vo_test

import (
	"errors"
	"testing"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func TestParseSource(t *testing.T) {
	cases := map[string]struct {
		text string
		want vo.Source
		ok   bool
	}{
		"a name and a port":     {text: "s3.amazonaws.com:443", want: vo.Source{Host: "s3.amazonaws.com", Port: 443}, ok: true},
		"an address and a port": {text: "203.0.113.7:9000", want: vo.Source{Host: "203.0.113.7", Port: 9000}, ok: true},
		"surrounding spaces":    {text: "  huggingface.co:443 ", want: vo.Source{Host: "huggingface.co", Port: 443}, ok: true},
		"no port":               {text: "s3.amazonaws.com"},
		"no host":               {text: ":443"},
		"port zero":             {text: "s3.amazonaws.com:0"},
		"port past the range":   {text: "s3.amazonaws.com:70000"},
		"port is not a number":  {text: "s3.amazonaws.com:https"},
		"empty":                 {text: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			got, err := vo.ParseSource(tc.text)

			if tc.ok && (err != nil || got != tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
			if !tc.ok && !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func TestASourceReadsBackTheWayItWasWritten(t *testing.T) {

	text := vo.Source{Host: "s3.amazonaws.com", Port: 443}.String()

	if text != "s3.amazonaws.com:443" {
		t.Fatalf("got %q, want the host and port a rule is built from", text)
	}
}
