//go:build testenvci

package citest

import (
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const mixedFleetBaselineImage = "devshard-versiond:0.2.15-v5"

// TestMixedFleetNoProxy is the §8.2 mixed fleet: versiond-0 on 0.2.15-v5,
// versiond-1 and versiond-router on this tree, same escrow, RPC on, both hops
// HTTP/1.1 :8080. Chat plus Attach run through the replica that owns the
// escrow; sticky healthz must still hit both images.
func TestMixedFleetNoProxy(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireNoProxyRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("mixed fleet pins only versiond-0; unset TESTENV_VERSIOND_IMAGE and TESTENV_VERSIOND_ROUTER_IMAGE")
	}
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootMixedVersiondStack(t, "citest-mixed-fleet-*", mixedFleetBaselineImage)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router", "mock-openai")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	requireNoProxyService(t, stack)
	require.Equal(t, mixedFleetBaselineImage, stack.ServiceImage(t, "versiond-0"))
	require.Equal(t, "devshard-versiond:latest", stack.ServiceImage(t, "versiond-1"))
	require.Equal(t, "devshard-versiond-router:latest", stack.ServiceImage(t, "versiond-router"))
	requireVersiondRPC(t, stack, "versiond-0")
	requireVersiondRPC(t, stack, "versiond-1")

	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	version := cfg.Versiond.VersionName
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	postGatewayChats(t, client, eps, cfg, "citest mixed fleet")
	first, _ := waitHostMetric(t, stack, cfg, 30*time.Second, "devshard_peer_rpc_requests_total", `endpoint="Chat"`, `result="ok"`)
	require.NotEmpty(t, first, "no child counted Chat ok")
	requireAttachOK(t, stack, cfg, first)
	t.Logf("P6.8.2 mixed fleet: Chat+Attach on %s (%s) via HTTP/1.1", first, stack.ServiceImage(t, first))

	// Each versiond's own :8080 must answer. The shared router may send this
	// escrow to only one replica; that does not take the other image's listen down.
	for _, id := range []string{"versiond-0", "versiond-1"} {
		out, err := stack.ComposeExecOutput(id, "wget", "-qO-", "http://127.0.0.1:8080/"+version+"/healthz")
		require.NoError(t, err, "%s /%s/healthz", id, version)
		require.Contains(t, out, "ok", "%s healthz body", id)
		t.Logf("P6.8.2 mixed fleet: %s (%s) HTTP/1.1 /%s/healthz ok", id, stack.ServiceImage(t, id), version)
	}
}

func requireVersiondRPC(t *testing.T, stack *harness.Stack, service string) {
	t.Helper()
	out, err := stack.ComposeExecOutput(service, "printenv", "DEVSHARD_RPC_SERVER_ENABLED")
	require.NoError(t, err)
	require.Equal(t, "true", strings.TrimSpace(out))
	h2, err := stack.ComposeExecOutput(service, "printenv", "DEVSHARD_RPC_H2_PORT")
	if err == nil && strings.TrimSpace(h2) != "" {
		t.Fatalf("%s DEVSHARD_RPC_H2_PORT=%q", service, strings.TrimSpace(h2))
	}
}

func requireAttachOK(t *testing.T, stack *harness.Stack, cfg *config.File, host string) {
	t.Helper()
	bodies := hostMetricBodies(t, stack, cfg)
	require.True(t, metricLine(bodies[host], "devshard_peer_rpc_attach_total", `result="ok"`),
		"%s missing Attach ok", host)
}
