package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
)

// gatewayTimeoutActions counts the timeout rounds the ledger holds, by what became of each one.
func gatewayTimeoutActions(t *testing.T, client *http.Client, statsURL string) map[string]uint64 {
	t.Helper()
	body := testutil.GetJSON(t, client, statsURL+"/api/v1/epochs/current/participants")
	participants, _ := body["participants"].([]any)
	actions := map[string]uint64{}
	for _, rawParticipant := range participants {
		participant, ok := rawParticipant.(map[string]any)
		if !ok {
			continue
		}
		counters, _ := participant["counters"].([]any)
		for _, rawCounter := range counters {
			counter, ok := rawCounter.(map[string]any)
			if !ok {
				continue
			}
			action, _ := counter["timeout_action"].(string)
			if action == "" {
				continue
			}
			reason, _ := counter["timeout_reason"].(string)
			actions[action+"/"+reason] += testutil.NumericField(t, counter, "count")
		}
	}
	return actions
}

// Test flow:
//  1. Start the three-host environment with short protocol deadlines.
//  2. Send traffic, then stop one host and restart the other two without their storage.
//  3. Send more traffic so the stopped host's nonces need a timeout vote the others cannot answer.
//  4. Poll until a timeout round is recorded as failed.
//  5. Assert its nonces still reached a disposition rather than leaving the books.
func TestE2E_GatewayAccountsATimeoutItsVerifiersCouldNotDecide(t *testing.T) {
	requireSlowE2E(t)
	env, client := startGatewayEnv(t, e2eEnvOptions{
		mockChainParams: map[string]any{"refusal_timeout": 5, "execution_timeout": 10},
		hostEnvOverrides: map[int]map[string]string{
			0: shortDeadlines(nil),
			1: shortDeadlines(nil),
			2: shortDeadlines(nil),
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	for request := range 6 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("before the verifiers forget %d", request), testutil.AdminAPIKey)
	}

	env.stopHost(ctx, t, 1)
	env.restartHost(ctx, t, 0)
	env.restartHost(ctx, t, 2)

	for request := range 6 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("after the verifiers forget %d", request), testutil.AdminAPIKey)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for {
		actions := gatewayTimeoutActions(t, client, env.statsURL)
		if failedRounds(actions) > 0 {
			t.Logf("timeout rounds by outcome: %v", actions)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no timeout round was recorded as failed; the rounds that landed were %v", actions)
		}
		time.Sleep(3 * time.Second)
	}

	ledger := gatewayLedger(t, client, env.statsURL)
	var recorded uint64
	for _, disposition := range []string{"finished_used", "finished_unused", "finished_usage_unknown",
		"unfinished_refused", "unfinished_execution", "ghost"} {
		recorded += testutil.AccountingDispositionCount(ledger, disposition)
	}
	if recorded == 0 {
		t.Error("a round the verifiers could not decide took its nonces off the books entirely")
	}
}

func failedRounds(actions map[string]uint64) uint64 {
	var failed uint64
	for outcome, count := range actions {
		if len(outcome) >= 6 && outcome[:6] == "failed" {
			failed += count
		}
	}
	return failed
}

// Test flow:
//  1. Start the three-host environment with storage that a restart clears.
//  2. Serve traffic, then restart one host without its storage.
//  3. Assert the group re-syncs it rather than leaving it answering "session not found".
//  4. Assert serving continues.
func TestE2E_GatewayServesAfterAHostForgetsTheSession(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "before the amnesia", testutil.AdminAPIKey))

	env.restartHost(ctx, t, 1)

	awaitServed(t, client, env.clientURL, 90*time.Second)
	if codes := gatewayFindings(t, client, env.statsURL); len(codes) > 0 {
		t.Errorf("a host losing its storage raised findings %v, want none", codes)
	}
}

// awaitTimeoutOutcome polls the ledger until one timeout outcome appears, naming what it saw instead.
func awaitTimeoutOutcome(t *testing.T, client *http.Client, statsURL, outcome string, within time.Duration) testutil.AccountingParticipantsResponse {
	t.Helper()
	deadline := time.Now().Add(within)
	var last testutil.AccountingParticipantsResponse
	for time.Now().Before(deadline) {
		last = gatewayLedger(t, client, statsURL)
		if testutil.AccountingTimeoutOutcomeCount(last, outcome) > 0 {
			return last
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no timeout vote reached %s within %s: rounds were %v, execution=%d", outcome, within,
		gatewayTimeoutActions(t, client, statsURL),
		testutil.AccountingDispositionCount(last, "unfinished_execution"))
	return last
}

// Test flow:
//  1. Start the three-host environment with short protocol deadlines and one host stalling for good.
//  2. Send six completions so the stalled host receipts a nonce it never finishes.
//  3. Poll accounting until that nonce is filed as unfinished_execution.
//  4. Poll accounting until its timeout vote is recorded as applied, which is what returns the reservation.
func TestE2E_GatewayAppliesTheTimeoutOfAHostThatReceiptsAndStalls(t *testing.T) {
	requireSlowE2E(t)
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{
			"DEVSHARD_E2E":                          "1",
			e2econfig.RefusalTimeoutSecondsEnv:      "5",
			e2econfig.ExecutionTimeoutSecondsEnv:    "10",
			e2econfig.StreamingHardTimeoutMillisEnv: "5000",
		},
		mockChainParams: map[string]any{"refusal_timeout": 5, "execution_timeout": 10},
		hostEnvOverrides: map[int]map[string]string{
			0: shortDeadlines(nil),
			1: shortDeadlines(map[string]string{e2econfig.StubInferenceDelayMillisEnv: "600000"}),
			2: shortDeadlines(nil),
		},
	})

	for request := range 6 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("receipted then stalled %d", request), testutil.AdminAPIKey)
	}

	awaitDisposition(t, client, env.statsURL, "unfinished_execution", 4*time.Minute)
	settled := awaitTimeoutOutcome(t, client, env.statsURL, "applied", 4*time.Minute)

	t.Logf("applied=%d execution=%d used=%d",
		testutil.AccountingTimeoutOutcomeCount(settled, "applied"),
		testutil.AccountingDispositionCount(settled, "unfinished_execution"),
		testutil.AccountingDispositionCount(settled, "finished_used"))
}
