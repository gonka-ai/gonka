package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host environment.
//  2. Query accounting before user traffic to capture the initial assigned nonce total.
//  3. Send several successful non-streaming completions through devshardctl.
//  4. Wait until accounting shows new assigned nonces and at least one disposition.
//  5. Assert the accounting API response is coherent and every nonce is counted once.
func TestE2E_AccountingHappyPath(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeAssigned := testutil.AccountingAssignedTotal(before)

	testutil.SendCompletions(t, client, env.clientURL, "accounting happy path", 3)

	after := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingAssignedTotal(resp) > beforeAssigned &&
			testutil.AccountingDispositionTotal(resp) > 0
	})
	testutil.RequireAccountingResponseCoherent(t, after, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, after)
	require.Greater(t, testutil.AccountingAssignedTotal(after), beforeAssigned, "assigned nonce count should grow after gateway traffic")
	require.Greater(t, testutil.AccountingDispositionTotal(after), uint64(0), "gateway traffic should produce at least one disposition")
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Query accounting before user traffic to capture the initial assigned nonce total.
//  3. Send several successful non-streaming completions through devshardctl.
//  4. Wait until accounting shows dispositions for the consumed nonces.
//  5. Assert each participant-model record has no residual accounting gap:
//     dispositions plus live/residual buckets equal assigned nonces.
func TestE2E_AccountingNoResidualAfterFinishedTraffic(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeAssigned := testutil.AccountingAssignedTotal(before)

	testutil.SendCompletions(t, client, env.clientURL, "accounting no residual", 6)

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingAssignedTotal(resp) > beforeAssigned &&
			testutil.AccountingDispositionTotal(resp) > 0
	})
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

// Test flow:
//  1. Start the three-host environment with the nonce-1 host delaying inference.
//  2. Send one non-streaming completion asynchronously so its sent nonce stays live.
//  3. Poll accounting until the sent nonce appears in the in_flight bucket.
//  4. Assert in_flight is a current disposition bucket and accounting still balances.
//  5. Wait for the delayed completion and assert the user request still succeeds.
func TestE2E_AccountingLiveInFlightIsCountedOnce(t *testing.T) {
	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{
			1: {
				"DEVSHARD_STUB_INFERENCE_DELAY_MS": "3000",
			},
		},
	})

	_ = testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})

	done := make(chan testutil.RawResponseResult, 1)
	go func() {
		resp, err := testutil.SendCompletionRawE(client, env.clientURL, "accounting live in-flight", testutil.AdminAPIKey)
		done <- testutil.RawResponseResult{Response: resp, Err: err}
	}()

	inFlight := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 && testutil.AccountingInFlightTotal(resp) > 0
	})
	require.Greater(t, testutil.AccountingInFlightTotal(inFlight), uint64(0), "accounting should expose the live sent nonce as in_flight")
	testutil.RequireAccountingResponseCoherent(t, inFlight, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, inFlight)

	select {
	case result := <-done:
		require.NoError(t, result.Err)
		testutil.LogRawResponse(t, "accounting live in-flight completion", result.Response)
		testutil.RequireOpenAINonStreamingCompletion(t, result.Response)
	case <-time.After(testutil.DefaultRequestTimeout):
		t.Fatal("timed out waiting for delayed completion")
	}
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Stop the host assigned to the next nonce so the first attempt fails.
//  3. Send enough sequential completions for the failed participant to be skipped.
//  4. Poll accounting until the skipped no-send nonce appears as ghost.
//  5. Assert accounting remains coherent and every assigned nonce is counted once.
func TestE2E_AccountingFocusedGhostRequestNotSent(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeGhost := testutil.AccountingDispositionCount(before, "ghost")
	beforeAssigned := testutil.AccountingAssignedTotal(before)

	nextSlot := int((testutil.LatestSessionNonce(t, client, env.clientURL) + 1) % uint64(len(env.hostURLs)))
	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, nextSlot)

	testutil.SendCompletions(t, client, env.clientURL, "accounting focused ghost", len(env.hostURLs)+1)

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingAssignedTotal(resp) > beforeAssigned &&
			testutil.AccountingDispositionCount(resp, "ghost") > beforeGhost
	})
	require.Greater(t, testutil.AccountingDispositionCount(accounting, "ghost"), beforeGhost, "stopped participant should eventually be skipped as a ghost no-send nonce")
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Stop one devshard-host container.
//  3. Send a non-streaming completion through devshardctl and assert success.
//  4. Query accounting for the current epoch and model.
//  5. Assert accounting remains coherent and every assigned nonce is counted once.
func TestE2E_AccountingOneHostUnavailable(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, 1)

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "accounting one host unavailable", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "accounting one host unavailable completion", resp)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 && testutil.AccountingAssignedTotal(resp) > 0
	})
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}
