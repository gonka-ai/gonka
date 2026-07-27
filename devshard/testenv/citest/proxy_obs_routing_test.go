package citest

import (
	"strings"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestProxyObsRouting_VersionedAndVersionlessStats asserts join-proxy entrypoint
// routing for /devshard stats:
//   - versioned  /devshard/{v}/stats/… → versiond (catch-all /devshard/)
//   - versionless /devshard/stats/…     → dapi when EDGE_API_SERVICE_NAME empty,
//     edge-api (or edge-api-router) otherwise
func TestProxyObsRouting_VersionedAndVersionlessStats(t *testing.T) {
	t.Run("EDGE_API empty routes versionless stats to dapi", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "")
		require.Equal(t, "http://api_backend", dump.ObsProxyPass)
		require.Contains(t, dump.EdgeAPIUpstream, "# edge-api not configured")
		require.NotContains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.VersiondUpstream, "server versiond:8080")

		assertVersionlessStatsProxyPass(t, dump.DevshardLocations, "http://api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNoVersionedObsStripRewrite(t, dump.DevshardLocations)
	})

	t.Run("EDGE_API=edge-api routes versionless stats to edge-api", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "edge-api")
		require.Equal(t, "edge-api", dump.FinalEdgeAPIService)
		require.Equal(t, "http://edge_api_backend", dump.ObsProxyPass)
		require.Contains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.EdgeAPIUpstream, "server edge-api:18080")

		assertVersionlessStatsProxyPass(t, dump.DevshardLocations, "http://edge_api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNoVersionedObsStripRewrite(t, dump.DevshardLocations)
	})

	t.Run("EDGE_API=edge-api-router steers edge_api_backend at the router", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "edge-api-router")
		require.Equal(t, "edge-api-router", dump.FinalEdgeAPIService)
		require.Equal(t, "http://edge_api_backend", dump.ObsProxyPass,
			"versionless obs still uses edge_api_backend; the upstream host is the router")
		require.Contains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.EdgeAPIUpstream, "server edge-api-router:18080")
		require.NotContains(t, dump.EdgeAPIUpstream, "server edge-api:18080")

		assertVersionlessStatsProxyPass(t, dump.DevshardLocations, "http://edge_api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNoVersionedObsStripRewrite(t, dump.DevshardLocations)
	})
}

func assertVersionlessStatsProxyPass(t *testing.T, locations, wantPass string) {
	t.Helper()
	block := harness.LocationBlock(locations, "^/devshard/stats/shards")
	require.NotEmpty(t, block, "missing versionless /devshard/stats/shards location")
	require.Contains(t, block, "proxy_pass "+wantPass)
	require.NotContains(t, block, "versiond_backend")
}

func assertVersionedDevshardToVersiond(t *testing.T, locations string) {
	t.Helper()
	// Prefix match: exact "location /devshard/" catch-all (not /devshard/healthz).
	block := harness.LocationBlock(locations, "location /devshard/ {")
	if block == "" {
		block = harness.LocationBlock(locations, "location /devshard/\n")
	}
	require.NotEmpty(t, block, "missing catch-all location /devshard/")
	require.Contains(t, block, "proxy_pass http://versiond_backend/")
	// Versioned /devshard/vN/stats/shards must not have its own strip-to-versionless location.
	require.NotContains(t, locations, "^/devshard/[^/]+/stats/shards")
}

func assertNoVersionedObsStripRewrite(t *testing.T, locations string) {
	t.Helper()
	require.NotContains(t, locations, "rewrite ^/devshard/[^/]+/stats")
	require.NotContains(t, locations, "rewrite ^/devshard/[^/]+/sessions")
	require.NotContains(t, locations, "rewrite ^/devshard/[^/]+/metrics")
}

func TestProxyObsRouting_LocationBlockHelper(t *testing.T) {
	src := `
        location ~ ^/devshard/stats/shards(/.*)?$ {
            proxy_pass http://api_backend;
        }
        location /devshard/ {
            proxy_pass http://versiond_backend/;
        }`
	stats := harness.LocationBlock(src, "^/devshard/stats/shards")
	require.True(t, strings.Contains(stats, "proxy_pass http://api_backend"))
	require.False(t, strings.Contains(stats, "versiond_backend"))
	catch := harness.LocationBlock(src, "location /devshard/ {")
	require.Contains(t, catch, "versiond_backend")
}
