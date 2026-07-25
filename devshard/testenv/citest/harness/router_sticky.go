package harness

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// StickyUpstreamHeader is nginx $upstream_addr exposed by versiond-router.
const StickyUpstreamHeader = "X-Upstream-Addr"

// FindDistinctStickySessions returns two session ids routed to different versiond upstreams.
func FindDistinctStickySessions(t *testing.T, client *http.Client, routerHTTP, version string) (sessionA, upstreamA, sessionB, upstreamB string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	sessionA = "citest-failover-session-a"
	urlA := RouterSessionURL(routerHTTP, version, sessionA, "/healthz")
	upstreamA = RequireResponseHeader(t, client, urlA, StickyUpstreamHeader)

	for n := 0; n < 64; n++ {
		candidate := fmt.Sprintf("citest-failover-%d", n)
		if candidate == sessionA {
			continue
		}
		urlB := RouterSessionURL(routerHTTP, version, candidate, "/healthz")
		got, err := GetResponseHeader(client, urlB, StickyUpstreamHeader)
		if err != nil {
			t.Fatalf("probe session %q: %v", candidate, err)
		}
		if got != "" && got != upstreamA {
			return sessionA, upstreamA, candidate, got
		}
	}
	t.Fatalf("could not find a second sticky upstream distinct from %q", upstreamA)
	return "", "", "", ""
}

// OtherStickyUpstream returns the candidate in {a,b} that is not primary.
// Addresses are compared after trimming; comma-separated tried-upstream lists
// are not accepted for primary (callers should pass a single sticky addr).
func OtherStickyUpstream(primary, a, b string) string {
	primary = strings.TrimSpace(primary)
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case primary == a && a != b:
		return b
	case primary == b && a != b:
		return a
	default:
		return ""
	}
}

// HostIDForUpstream maps nginx upstream addr (host:port) to compose service id.
func HostIDForUpstream(cfg *config.File, upstreamAddr string) string {
	if cfg == nil {
		return ""
	}
	host := strings.TrimSpace(upstreamAddr)
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	for _, h := range cfg.Hosts {
		if h.IP == host {
			return h.ID
		}
	}
	return ""
}

// RequireGETStatusIn asserts GET returns one of the allowed HTTP status codes.
func RequireGETStatusIn(t *testing.T, client *http.Client, url string, allowed ...int) int {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	for _, code := range allowed {
		if resp.StatusCode == code {
			return resp.StatusCode
		}
	}
	t.Fatalf("GET %s: status %d not in %v", url, resp.StatusCode, allowed)
	return resp.StatusCode
}

// RequireGETNotGatewayError asserts the router still reaches a live versiond upstream.
func RequireGETNotGatewayError(t *testing.T, client *http.Client, url, wantUpstream string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusBadGateway, resp.StatusCode,
		"expected live upstream, got 502 from %s", url)
	require.NotEqual(t, http.StatusServiceUnavailable, resp.StatusCode,
		"expected live upstream, got 503 from %s", url)
	got := resp.Header.Get(StickyUpstreamHeader)
	require.Equal(t, wantUpstream, got, "sticky upstream changed for %s", url)
}

// WaitStickyFailoverToSurvivor polls until a session pinned to a stopped versiond is
// served by the surviving upstream (proxy_next_upstream on first 502 / connect fail).
// X-Upstream-Addr may list every tried peer (comma-separated); the survivor must appear
// and the response must not be a gateway error.
func WaitStickyFailoverToSurvivor(t *testing.T, client *http.Client, url, stoppedUpstream, survivorUpstream string, wait time.Duration) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	ok := AssertEventually(t, wait, time.Second, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable {
			return false
		}
		upstream := resp.Header.Get(StickyUpstreamHeader)
		return upstreamContains(upstream, survivorUpstream) && !upstreamOnly(upstream, stoppedUpstream)
	})
	require.True(t, ok,
		"session on stopped upstream %s did not failover to survivor %s within %s",
		stoppedUpstream, survivorUpstream, wait)
}

func upstreamContains(header, addr string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == addr {
			return true
		}
	}
	return false
}

func upstreamOnly(header, addr string) bool {
	parts := strings.Split(header, ",")
	if len(parts) != 1 {
		return false
	}
	return strings.TrimSpace(parts[0]) == addr
}
