//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestHAStaleStandbyCatchupIdempotent verifies HA failover onto a versiond whose
// in-memory escrow nonce lags shared Postgres: sticky primary advances PG, then
// is stopped; catch-up on the stale standby must succeed without SQLSTATE 23505.
func TestHAStaleStandbyCatchupIdempotent(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-ha-stale-standby-*")
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	version := cfg.Versiond.VersionName
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	// Confirm both HA upstreams are sticky-addressable before the escrow scenario.
	_, upstreamA, _, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, version)
	require.NotEqual(t, upstreamA, upstreamB)

	model := config.PrimaryModelID(cfg)
	harness.Step(t, "warm escrow via gateway chat (sticky primary loads session)")
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest ha stale standby warm"},
		},
		MaxTokens: 16,
	})
	snapN := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.NotEmpty(t, snapN.EscrowID)
	require.Greater(t, snapN.LatestNonce, uint64(0), "expected durable nonce after warm chat")

	escrowSessionURL := harness.RouterSessionURL(eps.RouterHTTP, version, snapN.EscrowID, "/mempool")
	primaryUpstream := harness.RequireResponseHeader(t, client, escrowSessionURL, harness.StickyUpstreamHeader)
	primaryHost := harness.HostIDForUpstream(cfg, primaryUpstream)
	require.NotEmpty(t, primaryHost, "map escrow sticky upstream %q to host id", primaryUpstream)
	survivorUpstream := harness.OtherStickyUpstream(primaryUpstream, upstreamA, upstreamB)
	require.NotEmpty(t, survivorUpstream, "need a survivor distinct from primary %s", primaryUpstream)

	harness.Step(t, "escrow %s sticky → %s (%s); survivor %s; advancing PG on primary",
		snapN.EscrowID, primaryUpstream, primaryHost, survivorUpstream)

	const advanceChats = 3
	for i := 0; i < advanceChats; i++ {
		harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest ha stale standby advance %d", i)},
			},
			MaxTokens: 16,
		})
	}
	snapAdvanced := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snapN, snapAdvanced)
	require.Greater(t, snapAdvanced.LatestNonce, snapN.LatestNonce,
		"primary chats must advance durable nonce beyond warm snapshot")

	harness.Step(t, "stop sticky primary %s so catch-up lands on stale standby", primaryHost)
	stack.StopService(t, primaryHost)
	harness.WaitStickyFailoverToSurvivor(t, client, escrowSessionURL, primaryUpstream, survivorUpstream, 45*time.Second)

	harness.Step(t, "gateway chat after failover (standby must reconcile + apply without 23505)")
	harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest ha stale standby after failover"},
		},
		MaxTokens: 16,
	})
	snapAfter := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snapAdvanced, snapAfter)

	harness.RequireNoDiffDuplicateKeyInLogs(t, stack, "versiond-0", "versiond-1")
}
