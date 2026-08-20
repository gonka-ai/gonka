package vo

import (
	"fmt"
	"strings"

	"trainshard/internal/domain/shared"
)

const digestAlgo = "sha256:"

const digestHexLen = 64

const digestShortLen = 12

type ImageDigest string

func ParseImageDigest(s string) (ImageDigest, error) {
	ref := strings.TrimSpace(s)
	digest := ref
	if name, after, found := strings.Cut(ref, "@"); found {
		if name == "" {
			return "", fmt.Errorf("image digest %q: %w", s, shared.ErrValidation)
		}
		digest = after
	}
	hex, found := strings.CutPrefix(digest, digestAlgo)
	if !found || len(hex) != digestHexLen || !isLowerHex(hex) {
		return "", fmt.Errorf("image digest %q: %w", s, shared.ErrValidation)
	}
	return ImageDigest(ref), nil
}

func (d ImageDigest) String() string { return string(d) }

func (d ImageDigest) Short() string {
	_, hex, found := strings.Cut(string(d), digestAlgo)
	if !found || len(hex) < digestShortLen {
		return string(d)
	}
	return digestAlgo + hex[:digestShortLen]
}

func (d ImageDigest) IsZero() bool { return d == "" }

type ImageLayers []string

func (l ImageLayers) DerivesFrom(base ImageLayers) bool {
	if len(base) == 0 || len(l) < len(base) {
		return false
	}
	for i := range base {
		if l[i] != base[i] {
			return false
		}
	}
	return true
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
