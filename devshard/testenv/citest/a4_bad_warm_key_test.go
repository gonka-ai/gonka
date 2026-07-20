//go:build testenvci

package citest

import (
	"testing"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"
)

// TestA4_BadWarmKey verifies POST /testenv/grantees revokes the configured warm grantee
// on mock-chain and that devshardd rejects warm-key transport auth via versiond-router afterward.
func TestA4_BadWarmKey(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootAdversarialStack(t, "citest-a4-*")
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	mockDapi := harness.MockDAPIFromConfig(cfg)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	requireHosts(t, cfg, 2)

	granter := cfg.Hosts[0].Address
	warm := cfg.WarmGrantee.Address
	requireNonEmpty(t, granter, "host[0] address")
	requireNonEmpty(t, warm, "warm_grantee address")
	requireNonEmpty(t, cfg.WarmGrantee.PrivateKeyHex, "warm_grantee private_key_hex")

	baselineReq := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest a4 baseline before warm-key revoke"},
		},
		MaxTokens: 16,
	}
	harness.Step(t, "gateway chat succeeds before warm-key revocation")
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, baselineReq)

	harness.Step(t, "revoke warm key %s for granter %s via /testenv/grantees", warm, granter)
	harness.PatchTestenvGrantees(t, client, mockDapi.HTTP, adminface.GranteesRequest{
		GranterAddress: granter,
		Grantees:       []string{"gonka1badwarm000000000000000000000000000"},
	})
	harness.RequireWarmKeyRevoked(t, cfg, granter, warm)

	harness.Step(t, "warm-key signed gossip/nonce via router should be forbidden after revocation")
	harness.RequireWarmKeyTransportRejected(t, client, cfg, eps, cfg.WarmGrantee.PrivateKeyHex)
}

func requireHosts(t *testing.T, cfg *config.File, n int) {
	t.Helper()
	if len(cfg.Hosts) < n {
		t.Fatalf("expected >= %d hosts, got %d", n, len(cfg.Hosts))
	}
}

func requireNonEmpty(t *testing.T, value, label string) {
	t.Helper()
	if value == "" {
		t.Fatalf("missing %s", label)
	}
}
