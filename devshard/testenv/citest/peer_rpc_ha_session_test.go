//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestPeerRPCHASessionSpread is the HA session reproduction: two escrows
// that the router hashes to different children must both complete a height
// seed and a chat. The shared session store is what makes the second one
// ready; Watch for `_` does not land on the child that attached the door.
func TestPeerRPCHASessionSpread(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	requireNoProxyRPC(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-peerrpc-ha-session-*")
	client := harness.GatewayChatClient()
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	firstUp := sessionUpstream(t, client, eps.RouterHTTP, version, escrowID)
	postSpreadChat(t, client, eps.GatewayHTTP, adminKey, model, "citest ha session first escrow")

	var secondUp string
	for n := 0; n < 8; n++ {
		harness.PostAdminDeactivateDevshard(t, client, eps.GatewayHTTP, adminKey, escrowID)
		harness.WaitGatewayEscrowRetired(t, client, eps.GatewayHTTP, escrowID, 2*time.Minute)
		harness.PostAdminCreateEscrow(t, client, eps.GatewayHTTP, adminKey, model, 500_000)
		harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
		escrowID = harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
		up := sessionUpstream(t, client, eps.RouterHTTP, version, escrowID)
		if up != firstUp {
			secondUp = up
			postSpreadChat(t, client, eps.GatewayHTTP, adminKey, model, fmt.Sprintf("citest ha session escrow %s", escrowID))
			break
		}
	}
	require.NotEmpty(t, secondUp, "no escrow hashed to a different upstream than %s", firstUp)
	require.NotEqual(t, firstUp, secondUp)
}

func sessionUpstream(t *testing.T, client *http.Client, router, version, escrowID string) string {
	t.Helper()
	// A missing HTTP session is 404. The router still stamps the upstream
	// that the escrow hash selected, which is all this probe needs.
	url := harness.RouterSessionURL(router, version, escrowID, "/healthz")
	return harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
}

func postSpreadChat(t *testing.T, client *http.Client, gateway, adminKey, model, content string) {
	t.Helper()
	resp := harness.PostGatewayChatCompletion(t, client, gateway, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: content},
		},
		MaxTokens: 16,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
}
