package citest

import (
	"strings"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestProxyObsRouting_VersionedAndVersionlessStats asserts join-proxy entrypoint
// routing for /v1/devshard:
//   - versionless obs (/sessions|stats/shards|metrics) → dapi when
//     EDGE_API_SERVICE_NAME empty, edge-api (or edge-api-router) otherwise
//   - other /v1/devshard/* → rewrite to /devshard/v1/* → versiond
//   - versioned /devshard/{v}/… → versiond catch-all (no versionless /devshard/ obs)
func TestProxyObsRouting_VersionedAndVersionlessStats(t *testing.T) {
	t.Run("EDGE_API empty routes versionless obs to dapi", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "")
		require.Equal(t, "http://api_backend", dump.ObsProxyPass)
		require.Contains(t, dump.EdgeAPIUpstream, "# edge-api not configured")
		require.NotContains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.VersiondUpstream, "server versiond:8080")

		assertAllVersionlessObsProxyPass(t, dump.DevshardLocations, "http://api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNonObsV1DevshardRewritesToVersiond(t, dump.DevshardLocations)
	})

	t.Run("EDGE_API=edge-api routes versionless obs to edge-api", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "edge-api")
		require.Equal(t, "edge-api", dump.FinalEdgeAPIService)
		require.Equal(t, "http://edge_api_backend", dump.ObsProxyPass)
		require.Contains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.EdgeAPIUpstream, "server edge-api:18080")

		assertAllVersionlessObsProxyPass(t, dump.DevshardLocations, "http://edge_api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNonObsV1DevshardRewritesToVersiond(t, dump.DevshardLocations)
	})

	t.Run("EDGE_API=edge-api-router steers edge_api_backend at the router", func(t *testing.T) {
		dump := harness.DumpProxyRouting(t, "edge-api-router")
		require.Equal(t, "edge-api-router", dump.FinalEdgeAPIService)
		require.Equal(t, "http://edge_api_backend", dump.ObsProxyPass,
			"versionless obs still uses edge_api_backend; the upstream host is the router")
		require.Contains(t, dump.EdgeAPIUpstream, "upstream edge_api_backend")
		require.Contains(t, dump.EdgeAPIUpstream, "server edge-api-router:18080")
		require.NotContains(t, dump.EdgeAPIUpstream, "server edge-api:18080")

		assertAllVersionlessObsProxyPass(t, dump.DevshardLocations, "http://edge_api_backend")
		assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
		assertNonObsV1DevshardRewritesToVersiond(t, dump.DevshardLocations)
	})
}

func assertAllVersionlessObsProxyPass(t *testing.T, locations, wantPass string) {
	t.Helper()
	for _, needle := range []string{
		"^/v1/devshard/sessions/[^/]+/(diffs|mempool|signatures)",
		"^/v1/devshard/stats/shards",
		"^/v1/devshard/metrics",
	} {
		block := harness.LocationBlock(locations, needle)
		require.NotEmpty(t, block, "missing versionless obs location matching %s", needle)
		require.Contains(t, block, "proxy_pass "+wantPass, needle)
		require.NotContains(t, block, "versiond_backend", needle)
		require.NotContains(t, block, "rewrite", needle)
	}
}

func assertVersionedDevshardToVersiond(t *testing.T, locations string) {
	t.Helper()
	block := harness.LocationBlock(locations, "location /devshard/ {")
	if block == "" {
		block = harness.LocationBlock(locations, "location /devshard/\n")
	}
	require.NotEmpty(t, block, "missing catch-all location /devshard/")
	require.Contains(t, block, "proxy_pass http://versiond_backend/")
	// Versionless obs must not live under bare /devshard/ (only /v1/devshard/…).
	require.NotContains(t, locations, "^/devshard/stats/shards")
	require.NotContains(t, locations, "^/devshard/sessions/")
	require.NotContains(t, locations, "^/devshard/metrics")
}

func assertNonObsV1DevshardRewritesToVersiond(t *testing.T, locations string) {
	t.Helper()
	block := harness.LocationBlock(locations, "location /v1/devshard/ {")
	require.NotEmpty(t, block, "missing prefix location /v1/devshard/")
	require.Contains(t, block, "rewrite ^/v1/devshard/")
	require.Contains(t, block, "/devshard/v1/")
	require.NotContains(t, block, "proxy_pass",
		"prefix /v1/devshard/ must only rewrite; obs uses dedicated regex locations")
}

func TestProxyObsRouting_LocationBlockHelper(t *testing.T) {
	src := `
        location ~ ^/v1/devshard/sessions/[^/]+/(diffs|mempool|signatures)$ {
            proxy_pass http://api_backend;
        }
        location ~ ^/v1/devshard/stats/shards(/.*)?$ {
            proxy_pass http://api_backend;
        }
        location ~ ^/v1/devshard/metrics$ {
            proxy_pass http://api_backend;
        }
        location /v1/devshard/ {
            rewrite ^/v1/devshard/(.*)$ /devshard/v1/$1 last;
        }
        location /devshard/ {
            proxy_pass http://versiond_backend/;
        }`
	for _, needle := range []string{
		"^/v1/devshard/sessions/",
		"^/v1/devshard/stats/shards",
		"^/v1/devshard/metrics",
	} {
		block := harness.LocationBlock(src, needle)
		require.Contains(t, block, "proxy_pass http://api_backend", needle)
		require.NotContains(t, block, "versiond_backend", needle)
	}
	catch := harness.LocationBlock(src, "location /devshard/ {")
	require.Contains(t, catch, "versiond_backend")
	rew := harness.LocationBlock(src, "location /v1/devshard/ {")
	require.Contains(t, rew, "rewrite")
	require.True(t, strings.Contains(rew, "/devshard/v1/"))
}
