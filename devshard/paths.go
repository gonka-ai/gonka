package devshard

import (
	"fmt"
	"strings"

	"devshard/types"
)

const (
	LegacyRoutePrefix = "/v1/devshard"
)

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

// VersionForRoutePrefix maps an HTTP route prefix to the version tag used when
// creating a user-side session (state machine + optional SQLite row). This is
// not the same as the URL path alone:
//
//   - LegacyRoutePrefix (/v1/devshard): session tag is types.LegacyRouteSessionVersion
//     ("v1"), matching embedded dapi HostManager boundVersion (gm/microrelease
//     used types.LegacySessionVersion for the same value).
//   - VersionedRoutePrefix (/devshard/<name>): <name> is the versiond runtime
//     name (must match the host's boundVersion and storage session pin).
//
// HTTP clients still use routePrefix on transport.HTTPClient; this function
// only resolves the session/storage tag. See devshard/docs/protocol-version.md.

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

func ResolveHostRoutePrefix(protocol types.ProtocolVersion, routePrefix string) string {
	if routePrefix != "" {
		return routePrefix
	}
	if protocol == types.ProtocolV1 {
		return LegacyRoutePrefix
	}
	return VersionedRoutePrefix(ProtocolRouteVersion(protocol))
}

func VersionForRoutePrefix(routePrefix string) (string, error) {
	normalized := NormalizeRoutePrefix(routePrefix)
	if normalized == LegacyRoutePrefix {
		return types.LegacyRouteSessionVersion, nil
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
