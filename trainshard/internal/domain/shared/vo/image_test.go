package vo_test

import (
	"errors"
	"strings"
	"testing"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func TestParseImageDigest(t *testing.T) {
	hex := strings.Repeat("a", 64)

	cases := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "named reference with a digest", input: "ghcr.io/gonka/train@sha256:" + hex, valid: true},
		{name: "bare digest", input: "sha256:" + hex, valid: true},
		{name: "surrounding spaces", input: "  sha256:" + hex + "  ", valid: true},
		{name: "tag instead of a digest", input: "ghcr.io/gonka/train:v1"},
		{name: "digest too short", input: "sha256:" + strings.Repeat("a", 63)},
		{name: "uppercase hex", input: "sha256:" + strings.Repeat("A", 64)},
		{name: "unknown algorithm", input: "md5:" + hex},
		{name: "empty name before the digest", input: "@sha256:" + hex},
		{name: "empty", input: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			_, err := vo.ParseImageDigest(tc.input)

			if tc.valid && err != nil {
				t.Fatalf("got %v, want no error", err)
			}
			if !tc.valid && !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func TestImageLayersDerivesFrom(t *testing.T) {
	base := vo.ImageLayers{"one", "two"}

	cases := []struct {
		name  string
		child vo.ImageLayers
		want  bool
	}{
		{name: "layers added on top of the base", child: vo.ImageLayers{"one", "two", "three"}, want: true},
		{name: "the base itself", child: base, want: true},
		{name: "different first layer", child: vo.ImageLayers{"other", "two", "three"}},
		{name: "shorter than the base", child: vo.ImageLayers{"one"}},
		{name: "unrelated image", child: vo.ImageLayers{"x"}},
		{name: "no layers at all"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			got := tc.child.DerivesFrom(base)

			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImageLayersNeverDerivesFromAnUnknownBase(t *testing.T) {

	got := vo.ImageLayers{"one"}.DerivesFrom(nil)

	if got {
		t.Fatal("an image must not pass the base check when the base is unknown")
	}
}
