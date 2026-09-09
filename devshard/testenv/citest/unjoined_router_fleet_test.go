//go:build testenvci

package citest

import (
	"io"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestUnjoinedRouterFleet covers #1733 independently of the shared-network
// stack: real routers/versionds, file membership only, cross-router affinity.
func TestUnjoinedRouterFleet(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	fleet := harness.BootUnjoinedRouterFleet(t)
	client := &http.Client{Timeout: 3 * time.Second}
	sessionA, upstreamA, sessionB, upstreamB := harness.FindDistinctStickySessions(t, client, fleet.RouterHTTP[0], fleet.Version)
	require.ElementsMatch(t, []string{fleet.Endpoints[0].Address(), fleet.Endpoints[1].Address()}, []string{upstreamA, upstreamB})
	// Always stop versiond-0, regardless of which session the search found first.
	if upstreamA != fleet.Endpoints[0].Address() {
		sessionA, sessionB = sessionB, sessionA
		upstreamA, upstreamB = upstreamB, upstreamA
	}
	assertSticky := func(session, want string) {
		t.Helper()
		for _, router := range fleet.RouterHTTP {
			url := harness.RouterSessionURL(router, fleet.Version, session, "/healthz")
			for retry := 0; retry < 8; retry++ {
				resp, err := client.Get(url)
				require.NoError(t, err)
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				require.NoError(t, readErr)
				// devshardd exposes /healthz, but not /sessions/:id/healthz.
				// These synthetic paths exercise the router's session hash;
				// an upstream 404 is expected with today's application routes.
				// Boot separately requires HTTP 200 from each child's /healthz.
				require.Contains(t, []int{http.StatusOK, http.StatusNotFound}, resp.StatusCode,
					"router=%s session=%s retry=%d", router, session, retry)
				require.Equal(t, want, resp.Header.Get(harness.StickyUpstreamHeader), "router=%s session=%s retry=%d", router, session, retry)
			}
		}
	}
	harness.Step(t, "both routers must agree on %s -> %s and %s -> %s", sessionA, upstreamA, sessionB, upstreamB)
	assertSticky(sessionA, upstreamA)
	assertSticky(sessionB, upstreamB)

	harness.Step(t, "stop versiond-0; both routers must fail over within the same 45s window")
	fleet.Versionds.StopService(t, "versiond-0")
	deadline := time.Now().Add(45 * time.Second)
	for _, router := range fleet.RouterHTTP {
		remaining := time.Until(deadline)
		require.Positive(t, remaining, "both routers must converge within one failover window")
		url := harness.RouterSessionURL(router, fleet.Version, sessionA, "/healthz")
		harness.WaitStickyFailoverToSurvivor(t, client, url, upstreamA, upstreamB, remaining)
	}
	// Require final exact peer equality: a retry history alone must not satisfy
	// the older, deliberately permissive failover helper.
	assertSticky(sessionA, upstreamB)
	assertSticky(sessionB, upstreamB)
	harness.Step(t, "both routers converged on survivor %s; its existing session retained its mapping", upstreamB)
}
