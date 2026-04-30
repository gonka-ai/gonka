package devshard

import (
	"fmt"
	"strings"

	"devshard/types"
)

const LegacyRoutePrefix = "/v1/devshard"

func VersionedRoutePrefix(version string) string {
	return "/devshard/" + version
}

func NormalizeRoutePrefix(routePrefix string) string {
	if routePrefix == "" {
		return LegacyRoutePrefix
	}
	return routePrefix
}

func ResolveVersionedRoutePrefix(version, routePrefix string) string {
	if routePrefix != "" {
		return routePrefix
	}
	return VersionedRoutePrefix(version)
}

func VersionForRoutePrefix(routePrefix string) (string, error) {
	normalized := NormalizeRoutePrefix(routePrefix)
	if normalized == LegacyRoutePrefix {
		return types.LegacySessionVersion, nil
	}

	trimmed := strings.Trim(normalized, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 2 && parts[0] == "devshard" && parts[1] != "" {
		return parts[1], nil
	}

	return "", fmt.Errorf("unsupported devshard route prefix %q", routePrefix)
}

func SessionPayloadPath(routePrefix, escrowID string) string {
	normalized := strings.TrimPrefix(NormalizeRoutePrefix(routePrefix), "/")
	return fmt.Sprintf("%s/sessions/%s/payloads", normalized, escrowID)
}

func LegacySessionPayloadPath(escrowID string) string {
	return SessionPayloadPath(LegacyRoutePrefix, escrowID)
}

func VersionedSessionPayloadPath(version, escrowID string) string {
	return SessionPayloadPath(VersionedRoutePrefix(version), escrowID)
}
