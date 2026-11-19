package e2e

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"devshard/e2e/testutil"
)

// The gateway scenarios drive the devshardctl stack with cmd/gateway as the binary under test, so a
// difference in outcome is a difference in the gateway. This file holds the harness they share.
func startGatewayEnv(t *testing.T, opts e2eEnvOptions) (*e2eEnv, *http.Client) {
	t.Helper()
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	opts.runGateway = true
	images := requireGatewayImage(t, requiredImages(t))
	env := startE2EEnv(ctx, t, images, opts)
	return env, &http.Client{Timeout: testutil.DefaultRequestTimeout}
}

// A scenario that has to wait out a protocol deadline runs in minutes, not seconds, so it stays out of
// the default suite: the rest of the gateway set finishes in three minutes and must keep doing so.
func requireSlowE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("DEVSHARD_E2E_SLOW") != "1" {
		t.Skip("set DEVSHARD_E2E_SLOW=1 to run the gateway scenarios that wait out a protocol deadline")
	}
}

// gatewaySessionNonce reads escrow state through the gateway: addressed by id, admin-gated, flat.
func gatewaySessionNonce(t *testing.T, client *http.Client, clientURL string) uint64 {
	t.Helper()
	state := testutil.GetJSON(t, client, clientURL+"/devshard/"+defaultEscrowID+"/v1/state")
	return testutil.NumericField(t, state, "latest_nonce")
}

// gatewayLedger reads the nonce ledger once. It is the only account of where the money went: a client
// request spends several nonces, and some nonces belong to no request at all.
func gatewayLedger(t *testing.T, client *http.Client, statsURL string) testutil.AccountingParticipantsResponse {
	t.Helper()
	return testutil.WaitAccountingParticipants(t, client, statsURL, "",
		func(testutil.AccountingParticipantsResponse) bool { return true })
}

func gatewayLedgerOnceFinishesLand(t *testing.T, client *http.Client, env *e2eEnv,
	ready func(testutil.AccountingParticipantsResponse) bool,
) testutil.AccountingParticipantsResponse {
	t.Helper()
	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "carries the last finish", testutil.AdminAPIKey))
	return testutil.WaitAccountingParticipants(t, client, env.statsURL, "", ready)
}

// gatewayFindings collects every finding the ledger raised. A healthy run raises none: a finding is this
// gateway's own reading that something is wrong with a host or with its own counts.
func gatewayFindings(t *testing.T, client *http.Client, statsURL string) []string {
	t.Helper()
	body := testutil.GetJSON(t, client, statsURL+"/api/v1/epochs/current/participants")
	participants, _ := body["participants"].([]any)
	var codes []string
	for _, raw := range participants {
		participant, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		findings, _ := participant["findings"].([]any)
		for _, finding := range findings {
			if entry, ok := finding.(map[string]any); ok {
				codes = append(codes, entry["code"].(string))
			}
		}
	}
	return codes
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send one non-streaming completion.
//  3. Assert the whole stack answered it in OpenAI shape.
func TestE2E_GatewayServesTheStack(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "gateway smoke", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "gateway smoke completion", resp)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)
}

func gatewayGhostBurns(t *testing.T, client *http.Client, statsURL, reason string) uint64 {
	t.Helper()
	body := testutil.GetJSON(t, client, statsURL+"/api/v1/epochs/current/participants")
	participants, _ := body["participants"].([]any)
	var total uint64
	for _, rawParticipant := range participants {
		participant, ok := rawParticipant.(map[string]any)
		if !ok {
			continue
		}
		counters, _ := participant["counters"].([]any)
		for _, rawCounter := range counters {
			counter, ok := rawCounter.(map[string]any)
			if !ok || counter["disposition"] != "ghost" || counter["ghost_reason"] != reason {
				continue
			}
			total += testutil.NumericField(t, counter, "count")
		}
	}
	return total
}
