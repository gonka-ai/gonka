//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
)

const stickyUpstreamHeader = "X-Upstream-Addr"

// TestS2_RouterStickiness asserts nginx consistent-hash routes the same session id
// to the same versiond upstream across retries, and that two backends are reachable.
func TestS2_RouterStickiness(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootS1Stack(t, "citest-s2-*")
	client := harness.HTTPClient()
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)

	version := cfg.Versiond.VersionName
	sessionA := "citest-sticky-session-a"
	urlA := harness.RouterSessionURL(eps.RouterHTTP, version, sessionA, "/healthz")

	harness.Step(t, "same session %q retries stick to one upstream", sessionA)
	var backend string
	const retries = 8
	for i := 0; i < retries; i++ {
		got := harness.RequireResponseHeader(t, client, urlA, stickyUpstreamHeader)
		if i == 0 {
			backend = got
		} else if got != backend {
			t.Fatalf("retry %d: upstream %q != first upstream %q", i, got, backend)
		}
	}

	harness.Step(t, "find a second session id that lands on a different upstream")
	var otherBackend string
	for n := 0; n < 64; n++ {
		sessionB := fmt.Sprintf("citest-sticky-%d", n)
		if sessionB == sessionA {
			continue
		}
		urlB := harness.RouterSessionURL(eps.RouterHTTP, version, sessionB, "/healthz")
		got, err := harness.GetResponseHeader(client, urlB, stickyUpstreamHeader)
		if err != nil {
			t.Fatalf("probe session %q: %v", sessionB, err)
		}
		if got != "" && got != backend {
			otherBackend = got
			harness.Step(t, "session %q → %s (distinct from %q → %s)", sessionB, got, sessionA, backend)
			break
		}
	}
	if otherBackend == "" {
		t.Fatalf("could not find a session id routed to a different upstream than %q", backend)
	}
}
