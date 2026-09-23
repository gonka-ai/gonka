//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestPeerRPCChat is citest-peerrpc-chat: gateway chat (non-stream and SSE)
// over Connect HTTP/1.1 on versiond-router:8080. No proxy, no h2 port.
// The child must count Attach and Chat; a JSON Send would not.
func TestPeerRPCChat(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireNoProxyRPC(t)
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-peerrpc-chat-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router", "mock-openai")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	requireNoProxyService(t, stack)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	postGatewayChats(t, client, eps, cfg, "citest peerrpc chat")

	host, _ := waitHostMetric(t, stack, cfg, 30*time.Second, "devshard_peer_rpc_requests_total", `endpoint="Chat"`, `result="ok"`)
	require.NotEmpty(t, host, "no child counted Chat ok; gateway Send stayed on HTTP")
	bodies := hostMetricBodies(t, stack, cfg)
	require.True(t, metricLine(bodies[host], "devshard_peer_rpc_attach_total", `result="ok"`),
		"%s Chat ok without Attach ok", host)
	t.Logf("P6.8.2 peerrpc chat: Attach+Chat on %s via HTTP/1.1", host)
}
