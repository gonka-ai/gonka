//go:build testenvci

package citest

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// TestRetiredSessionHTTP checks the decommissioned Echo session routes.
// Ops and catalog stay. Gateway chat is native gRPC on versiond-router:8081.
// There is no proxy container. POST chat/completions is 410.
func TestRetiredSessionHTTP(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireNoProxyGRPC(t)
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-retired-session-http-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-router")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	requireNoProxyService(t, stack)

	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router /healthz", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 2*time.Minute, "catalog admission", stack)
	waitGETStatus(t, client, eps.RouterHTTP+"/"+version+"/clock", 2*time.Minute, http.StatusNoContent, "child /clock", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/devshard/metrics", 2*time.Minute, "child /metrics", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/devshard/stats/shards", 2*time.Minute, "stats shards", stack)

	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	postGatewayChats(t, client, eps, cfg, "retired session http chat")

	escrow := config.PrimaryEscrowID(cfg)
	ops, err := client.Get(eps.RouterHTTP + "/devshard/sessions/" + escrow + "/signatures?nonce=1")
	require.NoError(t, err)
	opsBody, err := io.ReadAll(io.LimitReader(ops.Body, 4096))
	ops.Body.Close()
	require.NoError(t, err)
	require.NotEqual(t, http.StatusGone, ops.StatusCode, "ops GET retired: %s", opsBody)
	require.NotEqual(t, transport.DevshardErrorHTTPSessionRetired, ops.Header.Get(transport.HeaderDevshardError))

	url := eps.RouterHTTP + "/devshard/" + version + "/sessions/" + escrow + "/chat/completions"
	resp, err := client.Post(url, "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	require.NoError(t, err)
	require.Equal(t, http.StatusGone, resp.StatusCode, "body: %s", body)
	require.Equal(t, transport.DevshardErrorHTTPSessionRetired, resp.Header.Get(transport.HeaderDevshardError))
	require.Contains(t, string(body), "Connect")
}

func waitGETStatus(t *testing.T, client *http.Client, url string, timeout time.Duration, want int, label string, stack *harness.Stack) {
	t.Helper()
	t.Logf("citest: waiting for %s → %s (HTTP %d, timeout %s)", label, url, want, timeout)
	var attempts int
	var lastErr string
	ok := harness.AssertEventually(t, timeout, 2*time.Second, func() bool {
		attempts++
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err.Error()
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode != want {
			lastErr = http.StatusText(resp.StatusCode)
			if lastErr == "" {
				lastErr = strings.TrimSpace(resp.Status)
			}
			return false
		}
		return true
	})
	if !ok {
		if stack != nil {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-router", "devshardctl")
		}
		t.Fatalf("citest: %s not ready after %d attempts (%s): %s", label, attempts, url, lastErr)
	}
}
