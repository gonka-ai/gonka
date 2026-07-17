package devshard

import (
	"fmt"
	"strings"
)

func VersionedRoutePrefix(version string) string {
	return "/devshard/" + version
}

// VersionForRoutePrefix maps a versioned HTTP route prefix to the runtime tag
// used when creating a user-side session.
func ResolveRoutePrefix(routePrefix string) (string, string, error) {
	normalized := strings.TrimRight(strings.TrimSpace(routePrefix), "/")
	if !strings.HasPrefix(normalized, "/") {
		return "", "", fmt.Errorf("unsupported devshard route prefix %q", routePrefix)
	}
	parts := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	if len(parts) == 2 && parts[0] == "devshard" && parts[1] != "" {
		return normalized, parts[1], nil
	}

	return "", "", fmt.Errorf("unsupported devshard route prefix %q", routePrefix)
}

func VersionForRoutePrefix(routePrefix string) (string, error) {
	_, version, err := ResolveRoutePrefix(routePrefix)
	if err != nil {
		return "", err
	}
	return version, nil
}

func SessionPayloadPath(routePrefix, escrowID string) string {
	normalized := strings.TrimPrefix(routePrefix, "/")
	return fmt.Sprintf("%s/sessions/%s/payloads", normalized, escrowID)
}

func VersionedSessionPayloadPath(version, escrowID string) string {
	return SessionPayloadPath(VersionedRoutePrefix(version), escrowID)
}
