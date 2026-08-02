//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

const (
	versiondBackendHeader = "X-Versiond-Backend"
	backendLegacy         = "versiond_legacy"
	backendHA             = "versiond_ha_pool"
	// legacyVersionPath is listed in VERSIOND_NON_HA_VERSIONS (gencompose: "v1").
	legacyVersionPath = "v1"
)

// TestLegacyVersionPinnedToSingleHost asserts versiond-router sends
// VERSIOND_NON_HA_VERSIONS prefixes only to VERSIOND_LEGACY_HOST, while other
// versions sticky-hash across the pool (see pr-1366-deploy-test-plan.md §3.2).
func TestLegacyVersionPinnedToSingleHost(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-legacy-version-pin-*")
	client := harness.HTTPClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)

	legacyHostID := cfg.Hosts[0].ID
	haVersion := cfg.Versiond.VersionName
	require.NotEqual(t, legacyVersionPath, haVersion,
		"legacy probe path must differ from HA VersionName (%s)", haVersion)

	harness.Step(t, "legacy version %q must always pin to %s (versiond_legacy)", legacyVersionPath, legacyHostID)
	var legacyUpstream string
	const legacyProbes = 16
	for i := 0; i < legacyProbes; i++ {
		sessionID := fmt.Sprintf("citest-legacy-%d", i)
		url := harness.RouterSessionURL(eps.RouterHTTP, legacyVersionPath, sessionID, "/healthz")

		backend := harness.RequireResponseHeader(t, client, url, versiondBackendHeader)
		require.Equal(t, backendLegacy, backend,
			"legacy session %q: X-Versiond-Backend", sessionID)

		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		gotHost := harness.HostIDForUpstream(cfg, upstream)
		require.Equal(t, legacyHostID, gotHost,
			"legacy session %q: upstream %q should map to %s", sessionID, upstream, legacyHostID)

		if i == 0 {
			legacyUpstream = upstream
		} else {
			require.Equal(t, legacyUpstream, upstream,
				"legacy session %q: upstream drifted from first probe", sessionID)
		}
	}

	harness.Step(t, "HA version %q still sticky-hashes across the router pool", haVersion)
	_, upstreamA, _, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, haVersion)
	require.NotEqual(t, upstreamA, upstreamB)

	urlHA := harness.RouterSessionURL(eps.RouterHTTP, haVersion, "citest-legacy-version-pin-ha-check", "/healthz")
	haBackend := harness.RequireResponseHeader(t, client, urlHA, versiondBackendHeader)
	require.Equal(t, backendHA, haBackend, "HA path X-Versiond-Backend")

	nonLegacyHost := ""
	for _, h := range cfg.Hosts {
		if h.ID != legacyHostID {
			nonLegacyHost = h.ID
			break
		}
	}
	require.NotEmpty(t, nonLegacyHost, "need a non-legacy versiond host to stop")

	harness.Step(t, "stop non-legacy host %s; legacy pin must keep working", nonLegacyHost)
	stack.StopService(t, nonLegacyHost)

	for i := 0; i < 8; i++ {
		sessionID := fmt.Sprintf("citest-legacy-after-stop-%d", i)
		url := harness.RouterSessionURL(eps.RouterHTTP, legacyVersionPath, sessionID, "/healthz")
		backend := harness.RequireResponseHeader(t, client, url, versiondBackendHeader)
		require.Equal(t, backendLegacy, backend)
		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		require.Equal(t, legacyUpstream, upstream,
			"legacy upstream changed after stopping %s", nonLegacyHost)
		require.Equal(t, legacyHostID, harness.HostIDForUpstream(cfg, upstream))
	}
}
