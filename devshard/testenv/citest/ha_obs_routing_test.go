package citest

import (
	"regexp"
	"strings"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// Versionless obs endpoints as advertised to clients. Stateful ones carry an
// escrow/session id and must reach the versiond that owns that session;
// stateless ones are process-wide and may be served by any instance.
var (
	statefulObsLocations = []string{
		"^/v1/devshard/sessions/[^/]+/(diffs|mempool|signatures)",
	}
	statelessObsLocations = []string{
		"^/v1/devshard/stats/shards",
		"^/v1/devshard/metrics",
	}
)

// TestHAObsRouting_ProxyWithRouters asserts the join proxy in full HA mode:
// Tier A + versionless obs go to edge-api-router, and everything version-pinned
// goes to versiond-router, for stateful and stateless endpoints alike.
func TestHAObsRouting_ProxyWithRouters(t *testing.T) {
	dump := harness.DumpProxyRoutingEnv(t, harness.ProxyRoutingEnv{
		EdgeAPIService:  "edge-api-router",
		VersiondService: "versiond-router",
	})

	require.Equal(t, "edge-api-router", dump.FinalEdgeAPIService)
	require.Contains(t, dump.EdgeAPIUpstream, "server edge-api-router:18080")
	require.NotContains(t, dump.EdgeAPIUpstream, "server edge-api:18080")
	require.Contains(t, dump.VersiondUpstream, "server versiond-router:8080")
	require.NotContains(t, dump.VersiondUpstream, "server versiond:8080")

	// Versionless obs (both kinds) terminates at the edge-api pool, which then
	// fans out / looks up per instance — it must not be sent to versiond.
	for _, needle := range append(append([]string{}, statefulObsLocations...), statelessObsLocations...) {
		block := harness.LocationBlock(dump.DevshardLocations, needle)
		require.NotEmptyf(t, block, "missing versionless obs location %s", needle)
		require.Contains(t, block, "proxy_pass http://edge_api_backend", needle)
		require.NotContains(t, block, "versiond_backend", needle)
	}

	// Version-pinned obs and the protocol catch-all ride the sticky router.
	assertVersionedObsUsesDevshardObsZone(t, dump.DevshardLocations)
	assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
	assertNonObsV1DevshardRewritesToVersiond(t, dump.DevshardLocations)
}

// TestHAObsRouting_ProxyRouterVersiondWithDapiObs covers the HA variant where
// edge-api is not deployed: obs still terminates at dapi, while versioned
// traffic uses versiond-router.
func TestHAObsRouting_ProxyRouterVersiondWithDapiObs(t *testing.T) {
	dump := harness.DumpProxyRoutingEnv(t, harness.ProxyRoutingEnv{
		VersiondService: "versiond-router",
	})

	require.Equal(t, "http://api_backend", dump.ObsProxyPass)
	require.Contains(t, dump.VersiondUpstream, "server versiond-router:8080")
	assertAllVersionlessObsProxyPass(t, dump.DevshardLocations, "http://api_backend")
	assertVersionedObsUsesDevshardObsZone(t, dump.DevshardLocations)
	assertVersionedDevshardToVersiond(t, dump.DevshardLocations)
}

// TestHAObsRouting_VersiondRouterStickyOnSessionID asserts the sticky rule that
// makes stateful obs correct behind versiond-router: the hash key is the escrow
// id for /sessions/ paths (with or without the /devshard/ prefix), while
// stateless paths keep the default request-URI key.
func TestHAObsRouting_VersiondRouterStickyOnSessionID(t *testing.T) {
	conf := harness.RenderVersiondRouterConfig(t, harness.VersiondRouterEnv{
		Hosts: []string{"versiond", "versiond2", "versiond3"},
	})

	pool := harness.UpstreamBlock(conf, "versiond_ha_pool")
	require.NotEmpty(t, pool, "missing versiond_ha_pool upstream")
	require.Contains(t, pool, "hash $sticky_key consistent",
		"stateful session traffic must be sticky, not round-robin")
	for _, host := range []string{"versiond", "versiond2", "versiond3"} {
		require.Contains(t, pool, "server "+host+":8080", "HA pool must include %s", host)
	}
	require.Contains(t, pool, "max_fails=1", "a dead peer must drop out for failover")

	// $sticky_key defaults to the whole URI and narrows to the escrow id only
	// for session-scoped paths.
	require.Contains(t, conf, "set $sticky_key $request_uri;")
	stickyRe := regexp.MustCompile(`\$request_uri\s*~\s*"([^"]+)"`)
	m := stickyRe.FindStringSubmatch(conf)
	require.Len(t, m, 2, "missing sticky-key capture condition")

	sessionRe := regexp.MustCompile(m[1])
	for _, path := range []string{
		"/v4/sessions/escrow-1/diffs",
		"/devshard/v4/sessions/escrow-1/mempool",
		"/devshard/v4/sessions/escrow-1/signatures",
	} {
		got := sessionRe.FindStringSubmatch(path)
		require.Lenf(t, got, 2, "sticky key must capture the escrow id from %s", path)
		require.Equal(t, "escrow-1", got[1], path)
	}
	for _, path := range []string{
		"/v4/stats/shards",
		"/devshard/v4/metrics",
		"/devshard/v4/healthz",
	} {
		require.Nilf(t, sessionRe.FindStringSubmatch(path),
			"stateless path %s must not be pinned by session id", path)
	}

	// Failover only applies to retriable failures; a draining peer stays sticky.
	nextUpstream := directiveLine(t, conf, "proxy_next_upstream ")
	require.Contains(t, nextUpstream, "error timeout http_502")
	require.NotContains(t, nextUpstream, "http_503",
		"503 means drain or HA guard; retrying elsewhere would break stickiness")
}

// directiveLine returns the first non-comment config line starting with name.
func directiveLine(t *testing.T, conf, name string) string {
	t.Helper()
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, name) {
			return trimmed
		}
	}
	t.Fatalf("directive %q not found", name)
	return ""
}

// TestHAObsRouting_VersiondRouterLegacyVersionsPinToSQLiteHost covers mixed
// estates: pre-HA versions must land on the host owning their SQLite files,
// even for stateful obs, while newer versions use the HA pool.
func TestHAObsRouting_VersiondRouterLegacyVersionsPinToSQLiteHost(t *testing.T) {
	conf := harness.RenderVersiondRouterConfig(t, harness.VersiondRouterEnv{
		Hosts:         []string{"versiond", "versiond2"},
		LegacyHost:    "versiond",
		NonHAVersions: "v1 v2 v3",
	})

	legacy := harness.UpstreamBlock(conf, "versiond_legacy")
	require.NotEmpty(t, legacy)
	require.Contains(t, legacy, "server versiond:8080")
	require.NotContains(t, legacy, "server versiond2:8080")

	backend := harness.MapBlock(conf, "$versiond_backend")
	require.NotEmpty(t, backend, "missing $versiond_backend map")
	require.Contains(t, backend, "default versiond_ha_pool;")
	for _, ver := range []string{"v1", "v2", "v3"} {
		require.Containsf(t, backend, "    "+ver+" versiond_legacy;", "%s must pin to the SQLite host", ver)
	}
	require.NotContains(t, backend, "v4 versiond_legacy;", "unlisted versions stay HA")

	// Multi-host pools advertise HA to devshardd so it refuses SQLite storage.
	haHeader := harness.MapBlock(conf, "$devshard_ha_header")
	require.Contains(t, haHeader, `versiond_ha_pool "true";`)
}

// TestHAObsRouting_VersiondRouterSingleHostIsNotAdvertisedHA guards the
// single-instance case behind the same router image.
func TestHAObsRouting_VersiondRouterSingleHostIsNotAdvertisedHA(t *testing.T) {
	conf := harness.RenderVersiondRouterConfig(t, harness.VersiondRouterEnv{
		Hosts: []string{"versiond"},
	})

	haHeader := harness.MapBlock(conf, "$devshard_ha_header")
	require.NotEmpty(t, haHeader)
	require.NotContains(t, haHeader, `"true"`,
		"a one-host pool must not claim HA storage to devshardd")
}

// TestHAObsRouting_EdgeAPIRouterIsStateless asserts edge-api-router spreads
// versionless obs across every replica: no stickiness, all hosts in the pool.
func TestHAObsRouting_EdgeAPIRouterIsStateless(t *testing.T) {
	hosts := []string{"edge-api", "edge-api-2", "edge-api-3"}
	conf := harness.RenderEdgeAPIRouterConfig(t, hosts, "18080")

	pool := harness.UpstreamBlock(conf, "edge_api_pool")
	require.NotEmpty(t, pool, "missing edge_api_pool upstream")
	for _, host := range hosts {
		require.Contains(t, pool, "server "+host+":18080", "pool must include %s", host)
	}
	require.NotContains(t, pool, "hash ",
		"edge-api holds no session state; obs requests round-robin")
	require.NotContains(t, pool, "ip_hash")

	require.Contains(t, conf, "proxy_pass http://edge_api_pool;")
	require.Contains(t, conf, "listen 18080;")
}

// TestHAObsRouting_EndToEndPathShape ties the two hops together: what the proxy
// forwards for each obs endpoint is what the routers are configured to accept.
func TestHAObsRouting_EndToEndPathShape(t *testing.T) {
	dump := harness.DumpProxyRoutingEnv(t, harness.ProxyRoutingEnv{
		EdgeAPIService:  "edge-api-router",
		VersiondService: "versiond-router",
	})

	// Version-pinned obs reaches versiond-router unrewritten, so the router's
	// version map sees /devshard/{version}/… as its first segments.
	for _, needle := range []string{
		"^/devshard/[^/]+/sessions/[^/]+/(diffs|mempool|signatures)",
		"^/devshard/[^/]+/stats/shards",
		"^/devshard/[^/]+/metrics",
	} {
		block := harness.LocationBlock(dump.DevshardLocations, needle)
		require.NotEmptyf(t, block, "missing version-pinned obs location %s", needle)
		require.Contains(t, block, "proxy_pass http://versiond_backend", needle)
		require.NotContains(t, block, "rewrite", needle)
	}

	conf := harness.RenderVersiondRouterConfig(t, harness.VersiondRouterEnv{
		Hosts: []string{"versiond", "versiond2"},
	})
	require.Contains(t, conf, "rewrite ^/devshard/(.*)$ /$1 break;",
		"router strips the /devshard/ prefix before proxying to versiond")

	versionRe := regexp.MustCompile(`~\^/\(\?:devshard/\)\?\(\?<ver>\[\^/\]\+\)`)
	require.True(t, versionRe.MatchString(strings.ReplaceAll(conf, " ", "")),
		"router must derive the version from either /{v}/… or /devshard/{v}/…")
}
