//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestS6_VersiondRestartPersistence verifies gateway chat and session nonce/state survive
// versiond → devshardd restarts without regression (postgres-backed host recovery).
func TestS6_VersiondRestartPersistence(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootS1Stack(t, "citest-s6-restart-*")
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitS1Healthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	model := config.PrimaryModelID(cfg)
	snap0 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)

	harness.Step(t, "initial gateway chat establishes session %s", snap0.EscrowID)
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest s6 chat before restart"},
		},
		MaxTokens: 16,
	})
	snap1 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snap0, snap1)

	harness.Step(t, "restart %s and continue same session", cfg.Hosts[0].ID)
	harness.RestartService(t, stack, cfg.Hosts[0].ID)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, snap1.EscrowID)
	snapAfterOne := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionStable(t, snap1, snapAfterOne)

	harness.Step(t, "gateway chat after single versiond restart")
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest s6 chat after one restart"},
		},
		MaxTokens: 16,
	})
	snap2 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snapAfterOne, snap2)

	hostIDs := harness.VersiondHostIDs(cfg)
	harness.Step(t, "restart all versiond instances (%v) and continue same session", hostIDs)
	harness.RestartServices(t, stack, hostIDs...)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, snap2.EscrowID)
	snapAfterAll := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionStable(t, snap2, snapAfterAll)

	harness.Step(t, "gateway chat after all versiond restarts")
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest s6 chat after all restarts"},
		},
		MaxTokens: 16,
	})
	snap3 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snapAfterAll, snap3)
}
