package pocstream

import (
	"strings"

	"golang.org/x/mod/semver"
)

const MinStreamCapableVersion = "3.0.14"

func IsVersionStreamCapable(version string) bool {
	v := canonicalVersion(version)
	return semver.IsValid(v) && semver.Compare(v, canonicalVersion(MinStreamCapableVersion)) >= 0
}

func canonicalVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	if idx := strings.IndexByte(version, '-'); idx >= 0 {
		version = version[:idx]
	}
	return "v" + version
}
