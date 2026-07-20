package queryapitest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canonicalPublicReadOnlyRoutes is the public Tier A set always published by the
// proxy (EDGE_API_ROUTE_PATHS_DEFAULT). Must match openapi.yaml minus optional
// verify/debug paths.
var canonicalPublicReadOnlyRoutes = []string{
	"/v1/status",
	"/v1/versions",
	"/v1/models",
	"/v1/governance/models",
	"/v1/governance/models-legacy",
	"/v1/pricing",
	"/v1/participants",
	"/v1/participants/{address}",
	"/v1/epochs/{epoch}",
	"/v1/epochs/{epoch}/participants",
	"/v1/poc-batches/{epoch}",
	"/v1/restrictions/status",
	"/v1/restrictions/exemptions",
	"/v1/restrictions/exemptions/{id}/usage/{account}",
	"/v1/bls/epoch/{id}",
	"/v1/bls/epochs/{id}",
	"/v1/bls/signatures/{request_id}",
	"/v1/bridge/addresses",
}

// canonicalOptionalReadOnlyRoutes are CPU-heavy verify/debug helpers served by
// edge-api but private on the proxy unless EDGE_API_EXPOSE_OPTIONAL_ROUTES=true.
var canonicalOptionalReadOnlyRoutes = []string{
	"/v1/verify-proof",
	"/v1/verify-block",
	"/v1/debug/pubkey-to-addr/{pubkey}",
	"/v1/debug/verify/{height}",
}

// canonicalReadOnlyRoutes is the full edge-api OpenAPI surface (public + optional).
var canonicalReadOnlyRoutes = append(
	append([]string(nil), canonicalPublicReadOnlyRoutes...),
	canonicalOptionalReadOnlyRoutes...,
)

func TestReadOnlyRouteCount(t *testing.T) {
	assert.Len(t, canonicalPublicReadOnlyRoutes, 18)
	assert.Len(t, canonicalOptionalReadOnlyRoutes, 4)
	assert.Len(t, canonicalReadOnlyRoutes, 22)
}

func TestOpenAPIPathsMatchCanonicalReadOnlyRoutes(t *testing.T) {
	got := loadOpenAPIPaths(t)
	sort.Strings(got)
	want := append([]string(nil), canonicalReadOnlyRoutes...)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestProxyEntrypointPublicRoutesMatchCanonical(t *testing.T) {
	got := loadProxyEdgeAPIRoutePaths(t, "EDGE_API_ROUTE_PATHS_DEFAULT='")
	sort.Strings(got)
	want := append([]string(nil), canonicalPublicReadOnlyRoutes...)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestProxyEntrypointOptionalRoutesMatchCanonical(t *testing.T) {
	got := loadProxyEdgeAPIRoutePaths(t, "EDGE_API_OPTIONAL_ROUTE_PATHS_DEFAULT='")
	sort.Strings(got)
	want := append([]string(nil), canonicalOptionalReadOnlyRoutes...)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestProxyEntrypointOptionalRoutesPrivateByDefault(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	path := filepath.Join(repoRoot, "proxy", "entrypoint.sh")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	script := string(b)
	require.Contains(t, script, `EDGE_API_EXPOSE_OPTIONAL_ROUTES=${EDGE_API_EXPOSE_OPTIONAL_ROUTES:-false}`)
	require.Contains(t, script, "optional_edge_api_blocked_prefixes")
	require.NotContains(t, extractQuotedSection(script, "EDGE_API_ROUTE_PATHS_DEFAULT='"), "/v1/verify-proof")
	require.Contains(t, extractQuotedSection(script, "EDGE_API_OPTIONAL_ROUTE_PATHS_DEFAULT='"), "/v1/verify-proof")
}

func loadOpenAPIPaths(t *testing.T) []string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(file), "..", "openapi.yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^  (/v1/[^:]+):`)
	matches := re.FindAllStringSubmatch(string(b), -1)
	require.NotEmpty(t, matches)

	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		paths = append(paths, m[1])
	}
	return paths
}

func loadProxyEdgeAPIRoutePaths(t *testing.T, startMarker string) []string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	path := filepath.Join(repoRoot, "proxy", "entrypoint.sh")
	b, err := os.ReadFile(path)
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^(/v1/[^\s']+)`)
	section := extractQuotedSection(string(b), startMarker)
	matches := re.FindAllStringSubmatch(section, -1)
	require.NotEmpty(t, matches, "%s not found in proxy/entrypoint.sh", startMarker)

	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		paths = append(paths, strings.TrimSpace(m[1]))
	}
	return paths
}

func extractQuotedSection(script, start string) string {
	const end = "'"
	i := strings.Index(script, start)
	if i < 0 {
		return ""
	}
	rest := script[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
