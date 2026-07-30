//go:build testenvci

package citest

import (
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestHAObsRoutingLive_VersiondRouter exercises the rendered versiond-router
// against real nginx: session-scoped (stateful) obs endpoints must follow the
// escrow id to one versiond, while process-wide (stateless) ones are served by
// whichever peer the pool picks.
func TestHAObsRoutingLive_VersiondRouter(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-ha-obs-routing-*")
	client := harness.HTTPClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-router", "versiond-0", "versiond-1")
		}
	})
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)

	version := cfg.Versiond.VersionName
	sessionA, upstreamA, sessionB, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, version)
	harness.Step(t, "sticky: %s → %s, %s → %s", sessionA, upstreamA, sessionB, upstreamB)

	harness.Step(t, "stateful obs endpoints follow the escrow id, not the path")
	for _, suffix := range []string{"/diffs", "/mempool", "/signatures"} {
		for _, session := range []struct{ id, upstream string }{
			{sessionA, upstreamA},
			{sessionB, upstreamB},
		} {
			url := harness.RouterSessionURL(eps.RouterHTTP, version, session.id, suffix)
			got := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
			require.Equalf(t, session.upstream, got,
				"%s must stay on the versiond that owns session %s", suffix, session.id)
		}
	}

	harness.Step(t, "the /devshard/ prefixed form hashes to the same upstream")
	prefixed := eps.RouterHTTP + "/devshard/" + version + "/sessions/" + sessionA + "/mempool"
	require.Equal(t, upstreamA,
		harness.RequireResponseHeader(t, client, prefixed, harness.StickyUpstreamHeader),
		"router must strip /devshard/ before hashing and proxying")

	harness.Step(t, "stateless obs endpoints are served by the pool")
	for _, path := range []string{
		"/" + version + "/stats/shards",
		"/" + version + "/metrics",
		"/devshard/" + version + "/metrics",
	} {
		harness.RequireGETStatusIn(t, client, eps.RouterHTTP+path, http.StatusOK)
	}
}
