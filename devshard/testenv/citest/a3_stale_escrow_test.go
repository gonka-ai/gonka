//go:build testenvci

package citest

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestA3_StaleEscrow verifies POST /testenv/escrow marks the active escrow settled on mock-chain
// and that the gateway stack stops serving chat against the stale escrow.
func TestA3_StaleEscrow(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootAdversarialStack(t, "citest-a3-*")
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	mockDapi := harness.MockDAPIFromConfig(cfg)
	adminKey := harness.TestenvAdminAPIKey
	gatewayURL := eps.GatewayHTTP
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-chain", "mock-dapi", "versiond-0", "versiond-1")
		}
	})

	escrowID := harness.GetGatewayEscrowID(t, client, gatewayURL)
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		t.Fatalf("parse escrow_id %q: %v", escrowID, err)
	}

	model := config.PrimaryModelID(cfg)
	baselineReq := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest a3 baseline before stale escrow"},
		},
		MaxTokens: 16,
	}
	harness.Step(t, "gateway chat succeeds before forced settlement")
	harness.PostGatewayChatCompletion(t, chatClient, gatewayURL, adminKey, baselineReq)

	harness.Step(t, "settle escrow %d on mock-chain while 2× versiond stack is up", id)
	harness.PatchTestenvEscrowSettle(t, client, mockDapi.HTTP, id)
	harness.RequireEscrowSettledOnChain(t, cfg, id)
	harness.PatchAdversarialFastTimeouts(t, client, mockDapi.HTTP)

	staleReq := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest a3 stale escrow after forced settlement"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "gateway should not continue using stale settled escrow %d", id)
	status := harness.WaitGatewayChatExpectFailure(t, chatClient, gatewayURL, adminKey, staleReq, 2*time.Minute)
	if status == 0 {
		harness.Step(t, "observed gateway transport timeout after stale escrow settlement")
	} else {
		harness.Step(t, "observed gateway HTTP %d after stale escrow settlement", status)
	}
	require.NotEqual(t, http.StatusOK, status)

	stillSettled := harness.GetGatewayEscrowID(t, client, gatewayURL)
	require.Equal(t, escrowID, stillSettled, "gateway should not silently rotate to a new escrow after stale settlement")
	harness.RequireEscrowSettledOnChain(t, cfg, id)
}
