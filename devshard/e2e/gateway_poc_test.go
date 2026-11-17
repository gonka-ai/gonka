package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// setChainPhase moves the phase the public API reports, and optionally publishes the participant group
// with only the named addresses preserved.
func setChainPhase(ctx context.Context, t *testing.T, env *e2eEnv, request map[string]any) {
	t.Helper()
	host, err := env.mockChain.Host(ctx)
	require.NoError(t, err)
	port, err := env.mockChain.MappedPort(ctx, "9191/tcp")
	require.NoError(t, err)
	applied := testutil.PostJSONRaw(t, &http.Client{Timeout: testutil.DefaultRequestTimeout},
		"http://"+host+":"+port.Port()+"/testenv/phase", request, "")
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("setting the chain phase %v = %d %s", request, applied.StatusCode, applied.Body)
	}
}

// Test flow:
//  1. Start the three-host environment with the gateway's PoC request mode off.
//  2. Move the chain into proof of compute.
//  3. Assert the gateway stops serving while the hosts are proving.
//  4. Move the chain back and assert serving resumes.
func TestE2E_GatewayStopsServingWhileTheChainIsInPoC(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{"DEVSHARD_POC_REQUEST_MODE": "off"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "before poc", testutil.AdminAPIKey))

	setChainPhase(ctx, t, env, map[string]any{"phase": "PoCGenerate"})

	deadline := time.Now().Add(60 * time.Second)
	for attempt := 0; ; attempt++ {
		blocked := testutil.SendCompletionRaw(t, client, env.clientURL, fmt.Sprintf("during poc %d", attempt), testutil.AdminAPIKey)
		if blocked.StatusCode == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway still served during PoC: %d %s", blocked.StatusCode, blocked.Body)
		}
		time.Sleep(2 * time.Second)
	}

	scrape := gatewayScrape(t, client, env.clientURL)
	requireMetrics(t, scrape, []string{"devshard_gateway_chain_requests_blocked", "devshard_gateway_chain_block_reason"})

	setChainPhase(ctx, t, env, map[string]any{"phase": "Inference"})
	awaitServed(t, client, env.clientURL, 60*time.Second)
}

// Test flow:
//  1. Start the three-host environment with the gateway in relaxed PoC mode.
//  2. Move the chain into proof of compute preserving only some participants.
//  3. Send traffic while the phase holds.
//  4. Assert the unpreserved host is not sent work and its nonces are burned under their own reason.
func TestE2E_GatewayBurnsTheNoncesOfAHostPoCDidNotPreserve(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{"DEVSHARD_POC_REQUEST_MODE": "relaxed"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	participants := gatewayParticipants(t, client, env.clientURL)
	setChainPhase(ctx, t, env, map[string]any{
		"phase":                "PoCGenerate",
		"publish_participants": true,
		"preserved_addresses":  participants[:2],
	})

	served, deadline := 0, time.Now().Add(90*time.Second)
	var burned uint64
	for request := 0; time.Now().Before(deadline); request++ {
		if response := testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("relaxed poc %d", request), testutil.AdminAPIKey); response.StatusCode == http.StatusOK {
			served++
		}
		if burned = gatewayGhostBurns(t, client, env.statsURL, "poc_unavailable_host"); burned > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if served == 0 {
		t.Fatal("relaxed mode served nothing during a PoC: the preserved hosts should have carried it")
	}
	if burned == 0 {
		t.Errorf("no nonce was burned for the host PoC left out; the burns that landed were %v",
			gatewayGhostReasons(t, client, env.statsURL))
	}
}
