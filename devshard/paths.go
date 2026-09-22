package devshard

import (
	"fmt"
	"strings"

	"devshard/types"
)

func VersionedRoutePrefix(version string) string {
	return "/devshard/" + version
}

// DefaultRoutePrefix is the HTTP mount used when no explicit route prefix is set.
func DefaultRoutePrefix() string {
	return VersionedRoutePrefix(types.EffectiveStateRootAndProtocolVersion)
}

// ResolveVersionedRoutePrefix returns routePrefix when set, otherwise the
// versioned mount for version.
func ResolveVersionedRoutePrefix(version, routePrefix string) string {
	if routePrefix != "" {
		return routePrefix
	}
	return VersionedRoutePrefix(version)
}

// NormalizeRoutePrefix returns a canonical route prefix. Empty input defaults
// to DefaultRoutePrefix(); otherwise ResolveRoutePrefix must succeed.
func NormalizeRoutePrefix(routePrefix string) string {
	if strings.TrimSpace(routePrefix) == "" {
		return DefaultRoutePrefix()
	}
	normalized, _, err := ResolveRoutePrefix(routePrefix)
	if err != nil {
		return routePrefix
	}
	return normalized
}

// ResolveRoutePrefix parses a versioned HTTP route prefix into (prefix, version).
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
	_, version, err := ResolveRoutePrefix(NormalizeRoutePrefix(routePrefix))
	if err != nil {
		return "", err
	}
	return version, nil
}

func SessionPayloadPath(routePrefix, escrowID string) string {
	normalized := strings.TrimPrefix(NormalizeRoutePrefix(routePrefix), "/")
	return fmt.Sprintf("%s/sessions/%s/payloads", normalized, escrowID)
}

func VersionedSessionPayloadPath(version, escrowID string) string {
	return SessionPayloadPath(VersionedRoutePrefix(version), escrowID)
}

// VersionlessRoutePrefix is the canonical mount for public observability
// (no protocol version segment). Protocol traffic stays under VersionedRoutePrefix.
const VersionlessRoutePrefix = "/devshard"

// VersionlessSessionDiffsPath is GET /devshard/sessions/{id}/diffs.
func VersionlessSessionDiffsPath(escrowID string) string {
	return fmt.Sprintf("%s/sessions/%s/diffs", VersionlessRoutePrefix, escrowID)
}

// VersionlessSessionMempoolPath is GET /devshard/sessions/{id}/mempool.
func VersionlessSessionMempoolPath(escrowID string) string {
	return fmt.Sprintf("%s/sessions/%s/mempool", VersionlessRoutePrefix, escrowID)
}

// VersionlessSessionSignaturesPath is GET /devshard/sessions/{id}/signatures.
func VersionlessSessionSignaturesPath(escrowID string) string {
	return fmt.Sprintf("%s/sessions/%s/signatures", VersionlessRoutePrefix, escrowID)
}

// VersionlessStatsShardsPath is GET /devshard/stats/shards.
func VersionlessStatsShardsPath() string {
	return VersionlessRoutePrefix + "/stats/shards"
}

// VersionlessStatsShardDetailPath is GET /devshard/stats/shards/{id}.
func VersionlessStatsShardDetailPath(escrowID string) string {
	return fmt.Sprintf("%s/stats/shards/%s", VersionlessRoutePrefix, escrowID)
}

// VersionlessMetricsPath is GET /devshard/metrics.
func VersionlessMetricsPath() string {
	return VersionlessRoutePrefix + "/metrics"
}

// VersionlessHealthzPath is GET /devshard/healthz.
func VersionlessHealthzPath() string {
	return VersionlessRoutePrefix + "/healthz"
}

// RouterCatalogHealthzPath is GET /{version}/healthz on the versiond router.
// That is catalog admission, not a host inference request.
func RouterCatalogHealthzPath(version string) string {
	version = strings.Trim(strings.TrimSpace(version), "/")
	if version == "" {
		return ""
	}
	return "/" + version + "/healthz"
}
