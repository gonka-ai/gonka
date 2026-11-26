package e2e

import (
	"fmt"

	"testing"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send four non-streaming completions.
//  3. Send one more completion, which carries the last answer's finish.
//  4. Poll the ledger until it counts at least four finished_used nonces.
//  5. Assert ordinary traffic raised no findings.
func TestE2E_GatewayAccountsServedNonces(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	const served = 4
	for request := range served {
		testutil.RequireOpenAINonStreamingCompletion(t,
			testutil.SendCompletionRaw(t, client, env.clientURL, fmt.Sprintf("served %d", request), testutil.AdminAPIKey))
	}

	gatewayLedgerOnceFinishesLand(t, client, env, func(ledger testutil.AccountingParticipantsResponse) bool {
		return testutil.AccountingDispositionCount(ledger, "finished_used") >= served
	})
	if codes := gatewayFindings(t, client, env.statsURL); len(codes) > 0 {
		t.Errorf("ordinary traffic raised findings %v, want none", codes)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the finished_used count before any traffic.
//  3. Send one streaming completion.
//  4. Send one non-streaming completion, which carries the stream's finish.
//  5. Poll the ledger until finished_used grew, so the stream spent a nonce the ledger recorded.
//  6. Assert the streamed request raised no findings.
func TestE2E_GatewayAccountsAStreamedNonceLikeABufferedOne(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	before := testutil.AccountingDispositionCount(gatewayLedger(t, client, env.statsURL), "finished_used")
	testutil.RequireOpenAIStream(t, testutil.SendStreamingCompletion(t, client, env.clientURL, "streamed"))
	gatewayLedgerOnceFinishesLand(t, client, env, func(ledger testutil.AccountingParticipantsResponse) bool {
		return testutil.AccountingDispositionCount(ledger, "finished_used") > before
	})
	if codes := gatewayFindings(t, client, env.statsURL); len(codes) > 0 {
		t.Errorf("a streamed request raised findings %v, want none", codes)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send one completion and read the session nonce.
//  3. Send the identical completion again.
//  4. Assert the nonce did not move, so the cache hit reached no host.
func TestE2E_GatewayCacheHitSpendsNoNonce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "cached exactly", testutil.AdminAPIKey))
	afterFirst := gatewaySessionNonce(t, client, env.clientURL)

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "cached exactly", testutil.AdminAPIKey))
	afterSecond := gatewaySessionNonce(t, client, env.clientURL)

	if afterSecond != afterFirst {
		t.Fatalf("nonce moved %d -> %d on a repeated request: the cache hit still spent one", afterFirst, afterSecond)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send six completions, which is enough for speculation to produce a loser unaided.
//  3. Poll accounting until a finished_unused nonce appears.
//  4. Log the used and unused counts the race settled on.
func TestE2E_GatewayAccountsAnAnswerThatLostItsRace(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	for request := range 6 {
		testutil.RequireOpenAINonStreamingCompletion(t,
			testutil.SendCompletionRaw(t, client, env.clientURL,
				fmt.Sprintf("lost race %d", request), testutil.AdminAPIKey))
	}

	settled := testutil.WaitAccountingParticipants(t, client, env.statsURL, "",
		func(resp testutil.AccountingParticipantsResponse) bool {
			return testutil.AccountingDispositionCount(resp, "finished_unused") > 0
		})
	t.Logf("unused=%d used=%d",
		testutil.AccountingDispositionCount(settled, "finished_unused"),
		testutil.AccountingDispositionCount(settled, "finished_used"))
}
