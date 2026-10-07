//go:build testenvci

package citest

import (
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"

	"github.com/stretchr/testify/require"
)

// TestH2EscrowStickiness checks the peer path. Attach and GetDiffs for one
// escrow stay on one versiond, and Watch plus renewal Attach on "_" stay
// together. Both hops must return X-Upstream-Addr.
func TestH2EscrowStickiness(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireOverlayRPC(t)
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootProxyOverlayStack(t, "citest-h2-sticky-*")
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "proxy", "versiond-0", "versiond-1", "versiond-router")
		}
	})
	version := stackVersion(cfg)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health", stack)

	client := h2cClient()
	hops := []struct {
		name string
		base string
	}{
		{name: "versiond-router", base: stack.RouterH2HTTP(t)},
		{name: "proxy", base: stack.ProxyH2HTTP(t)},
	}
	for _, hop := range hops {
		t.Run(hop.name, func(t *testing.T) {
			assertH2EscrowStickiness(t, client, hop.base, version, config.PrimaryEscrowID(cfg))
		})
	}
}

func assertH2EscrowStickiness(t *testing.T, client *http.Client, base, version, escrowA string) {
	t.Helper()
	attach := rpcpbconnect.PeerAuthServiceAttachProcedure
	diffs := rpcpbconnect.SessionServiceGetDiffsProcedure
	watch := rpcpbconnect.PeerAuthServiceWatchProcedure

	upA := h2StickyUpstream(t, client, h2SessionRPC(base, version, escrowA, attach))
	for i := 0; i < 4; i++ {
		require.Equal(t, upA, h2StickyUpstream(t, client, h2SessionRPC(base, version, escrowA, attach)), "Attach retry")
		require.Equal(t, upA, h2StickyUpstream(t, client, h2SessionRPC(base, version, escrowA, diffs)), "GetDiffs")
	}

	var escrowB, upB string
	for n := 0; n < 64; n++ {
		candidate := "citest-h2-sticky-" + strconv.Itoa(n)
		got := h2StickyUpstream(t, client, h2SessionRPC(base, version, candidate, diffs))
		if got != upA {
			escrowB, upB = candidate, got
			break
		}
	}
	require.NotEmpty(t, escrowB, "two escrows never landed on different upstreams (first=%q)", upA)
	for i := 0; i < 4; i++ {
		require.Equal(t, upB, h2StickyUpstream(t, client, h2SessionRPC(base, version, escrowB, attach)))
		require.Equal(t, upB, h2StickyUpstream(t, client, h2SessionRPC(base, version, escrowB, diffs)))
	}

	door := h2StickyUpstream(t, client, h2SessionRPC(base, version, transport.HostRPCEscrowID, watch))
	for i := 0; i < 4; i++ {
		require.Equal(t, door, h2StickyUpstream(t, client, h2SessionRPC(base, version, transport.HostRPCEscrowID, watch)), "Watch on _")
		require.Equal(t, door, h2StickyUpstream(t, client, h2SessionRPC(base, version, transport.HostRPCEscrowID, attach)), "renewal Attach on _")
	}
}

func h2SessionRPC(base, version, escrow, procedure string) string {
	return base + "/" + version + "/sessions/" + escrow + procedure
}

func h2StickyUpstream(t *testing.T, client *http.Client, rawURL string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := client.Do(req)
	require.NoError(t, err, "POST %s", rawURL)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	got := resp.Header.Get(harness.StickyUpstreamHeader)
	require.NotEmpty(t, got, "POST %s status %d missing %s", rawURL, resp.StatusCode, harness.StickyUpstreamHeader)
	return got
}
