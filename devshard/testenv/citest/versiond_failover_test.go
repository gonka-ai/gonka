//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestVersiondStickySessionFailover verifies versiond-router fails over a sticky session to a
// surviving versiond on the first upstream 502 / connect failure when its preferred
// peer is stopped; sessions hashed to a live upstream keep working.
func TestVersiondStickySessionFailover(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-versiond-failover-*")
	client := harness.HTTPClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)

	version := cfg.Versiond.VersionName
	sessionA, upstreamA, sessionB, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, version)
	require.NotEqual(t, upstreamA, upstreamB)

	stoppedHost := harness.HostIDForUpstream(cfg, upstreamA)
	require.NotEmpty(t, stoppedHost, "map upstream %q to host id", upstreamA)
	harness.Step(t, "session %q → %s; session %q → %s; stopping %s",
		sessionA, upstreamA, sessionB, upstreamB, stoppedHost)

	stack.StopService(t, stoppedHost)

	urlA := harness.RouterSessionURL(eps.RouterHTTP, version, sessionA, "/healthz")
	urlB := harness.RouterSessionURL(eps.RouterHTTP, version, sessionB, "/healthz")

	// First 502 / connect failure → proxy_next_upstream to survivor (not sticky 502 forever).
	harness.Step(t, "session on stopped upstream should reroute to surviving upstream %s", upstreamB)
	harness.WaitStickyFailoverToSurvivor(t, client, urlA, upstreamA, upstreamB, 45*time.Second)

	// Surviving upstream still serves requests for sessions hashed to it.
	harness.Step(t, "session on live upstream should still route to %s", upstreamB)
	harness.RequireGETNotGatewayError(t, client, urlB, upstreamB)
}
