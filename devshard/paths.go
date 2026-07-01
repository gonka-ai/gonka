package devshard

import (
	"fmt"
	"strings"

	"devshard/types"
)

func VersionedRoutePrefix(version string) string {
	return "/devshard/" + version
}

func ResolveVersionedRoutePrefix(version, routePrefix string) string {
	if routePrefix != "" {
		return routePrefix
	}
	return VersionedRoutePrefix(version)
}

// VersionForRoutePrefix maps a versioned HTTP route prefix to the runtime tag
// used when creating a user-side session.

func ProtocolRouteVersion(protocol types.ProtocolVersion) string {
	if protocol == "" {
		protocol = types.ProtocolV1
	}
	version := string(protocol)
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func ProtocolSessionVersion(protocol types.ProtocolVersion) string {
	if protocol == "" {
		protocol = types.ProtocolV1
	}
	return ProtocolRouteVersion(protocol)
}

func VersionForRoutePrefix(routePrefix string) (string, error) {
	trimmed := strings.Trim(routePrefix, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 2 && parts[0] == "devshard" && parts[1] != "" {
		return parts[1], nil
	}

	return "", fmt.Errorf("unsupported devshard route prefix %q", routePrefix)
}

func SessionPayloadPath(routePrefix, escrowID string) string {
	normalized := strings.TrimPrefix(routePrefix, "/")
	return fmt.Sprintf("%s/sessions/%s/payloads", normalized, escrowID)
}

func VersionedSessionPayloadPath(version, escrowID string) string {
	return SessionPayloadPath(VersionedRoutePrefix(version), escrowID)
}
