//go:build testenvci

package citest

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// TestSessionRoutesRetiredKeepsOpsAndConnectChat boots the stack with the
// RPC server enabled. Catalog, health, clock, and shard stats answer.
// Gateway chat runs over Connect. Echo chat/completions returns 410.
func TestSessionRoutesRetiredKeepsOpsAndConnectChat(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	switch strings.TrimSpace(os.Getenv("DEVSHARD_RPC_SERVER_ENABLED")) {
	case "", "true", "1":
	default:
		t.Fatalf("DEVSHARD_RPC_SERVER_ENABLED must be on for Connect chat, got %q", os.Getenv("DEVSHARD_RPC_SERVER_ENABLED"))
	}

	stack, cfg, eps := harness.BootStack(t, "citest-session-routes-retired-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-router")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)

	version := cfg.Versiond.VersionName
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router /healthz", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 2*time.Minute, "catalog admission", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/clock", 2*time.Minute, "child /clock", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/devshard/stats/shards", 2*time.Minute, "stats shards", stack)

	postGatewayChats(t, client, eps, cfg, "connect chat")

	escrow := config.PrimaryEscrowID(cfg)
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
