//go:build testenvci

package citest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
)

const (
	versiondBackendHeader = "X-Versiond-Backend"
	backendLegacy         = "versiond_legacy"
	backendDynamicPrefix  = "versiond_dynamic_"
)

// TestLegacyVersionPinnedToSingleHost first proves that the governance catalog
// admits the running version into a dynamic pool, then pins that same version
// to VERSIOND_LEGACY_HOST and verifies the single-host contract.
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

	harness.Step(t, "governance version %q must be admitted into a dynamic pool", haVersion)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+haVersion+"/healthz", 5*time.Minute,
		"catalog-admitted devshardd route", stack)
	_, upstreamA, _, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, haVersion)
	require.NotEqual(t, upstreamA, upstreamB)

	urlHA := harness.RouterSessionURL(eps.RouterHTTP, haVersion, "citest-catalog-admission", "/healthz")
	haBackend := harness.RequireResponseHeader(t, client, urlHA, versiondBackendHeader)
	require.True(t, strings.HasPrefix(haBackend, backendDynamicPrefix),
		"HA path X-Versiond-Backend = %q, want a catalog-admitted dynamic pool", haBackend)

	harness.Step(t, "pin running version %q to the legacy host", haVersion)
	harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", fmt.Sprintf("%q", haVersion))
	stack.RecreateServices(t, "versiond-router")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+haVersion+"/healthz", 5*time.Minute,
		"legacy-pinned devshardd route", stack)

	harness.Step(t, "legacy version %q must always pin to %s (versiond_legacy)", haVersion, legacyHostID)
	var legacyUpstream string
	const legacyProbes = 16
	for i := 0; i < legacyProbes; i++ {
		sessionID := fmt.Sprintf("citest-legacy-%d", i)
		url := harness.RouterSessionURL(eps.RouterHTTP, haVersion, sessionID, "/healthz")

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
		url := harness.RouterSessionURL(eps.RouterHTTP, haVersion, sessionID, "/healthz")
		backend := harness.RequireResponseHeader(t, client, url, versiondBackendHeader)
		require.Equal(t, backendLegacy, backend)
		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		require.Equal(t, legacyUpstream, upstream,
			"legacy upstream changed after stopping %s", nonLegacyHost)
		require.Equal(t, legacyHostID, harness.HostIDForUpstream(cfg, upstream))
	}
}
