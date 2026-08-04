package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
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
//  1. Start the three-host environment with the next assigned host delayed.
//  2. Use pairwise warmup sampling to start one fast secondary immediately.
//  3. Send one non-streaming completion and wait for the delayed primary to publish finish.
//  4. Send a non-redundant follow-up request so the gateway commits the pending finish txs.
//  5. Poll accounting until the winner is finished_used and the delayed loser is finished_unused.
//  6. Assert the redundant request consumed two finished nonces and accounting still balances.
func TestE2E_AccountingOverscheduledLoserFinishes(t *testing.T) {
	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{
			1: {
				"DEVSHARD_STUB_INFERENCE_DELAY_MS": "800",
			},
		},
	})

	nextSlot := int((testutil.LatestSessionNonce(t, client, env.clientURL) + 1) % uint64(len(env.hostURLs)))
	require.Equal(t, 1, nextSlot, "test setup expects the delayed host to own the first traffic nonce")

	testutil.PostJSON(t, client, env.clientURL+"/v1/admin/settings", map[string]any{
		"redundancy": map[string]any{
			"speed_policy":                     "hybrid",
			"pairwise_max_proactive_attempts":  1,
			"pairwise_min_direct_comparisons":  1,
			"non_stream_no_content_timeout_ms": int64(3000),
			"non_stream_max_attempt_wait_ms":   int64(3500),
			"secondary_wait_after_winner_ms":   int64(2500),
		},
	})

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeAssigned := testutil.AccountingAssignedTotal(before)
	beforeUsed := testutil.AccountingDispositionCount(before, "finished_used")
	beforeUnused := testutil.AccountingDispositionCount(before, "finished_unused")

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "accounting overscheduled loser finishes", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "accounting overscheduled loser finishes completion", resp)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)

	time.Sleep(2 * time.Second)
	testutil.PostJSON(t, client, env.clientURL+"/v1/admin/settings", map[string]any{
		"redundancy": map[string]any{
			"speed_policy":       "legacy",
			"receipt_timeout_ms": int64(5000),
		},
	})
	flush := testutil.SendCompletionRaw(t, client, env.clientURL, "accounting overscheduled loser finish flush", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "accounting overscheduled loser finish flush completion", flush)
	testutil.RequireOpenAINonStreamingCompletion(t, flush)

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingAssignedTotal(resp) >= beforeAssigned+2 &&
			testutil.AccountingDispositionCount(resp, "finished_used") > beforeUsed &&
			testutil.AccountingDispositionCount(resp, "finished_unused") > beforeUnused
	})
	require.Greater(t, testutil.AccountingDispositionCount(accounting, "finished_used"), beforeUsed, "winner should be counted as finished_used")
	require.Greater(t, testutil.AccountingDispositionCount(accounting, "finished_unused"), beforeUnused, "finished redundant loser should be counted as finished_unused")
	require.GreaterOrEqual(t, testutil.AccountingAssignedTotal(accounting), beforeAssigned+2, "redundant send should consume separate nonces for winner and loser")
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
//  1. Start the three-host environment with the next assigned host delaying inference.
//  2. Shorten only this e2e run's protocol and redundancy timeout windows.
//  3. Send one non-streaming completion asynchronously and observe its sent nonce in-flight.
//  4. Let redundancy return a fast secondary winner while timeout handling runs for the slow send.
//  5. Poll accounting until in_flight drops and an unfinished disposition appears.
func TestE2E_AccountingLiveSendTimeout(t *testing.T) {
	const shortProtocolTimeoutSeconds = "1"

	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{
			0: {
				e2econfig.RefusalTimeoutSecondsEnv:   shortProtocolTimeoutSeconds,
				e2econfig.ExecutionTimeoutSecondsEnv: shortProtocolTimeoutSeconds,
			},
			1: {
				e2econfig.RefusalTimeoutSecondsEnv:   shortProtocolTimeoutSeconds,
				e2econfig.ExecutionTimeoutSecondsEnv: shortProtocolTimeoutSeconds,
				"DEVSHARD_STUB_INFERENCE_DELAY_MS":   "8000",
			},
			2: {
				e2econfig.RefusalTimeoutSecondsEnv:   shortProtocolTimeoutSeconds,
				e2econfig.ExecutionTimeoutSecondsEnv: shortProtocolTimeoutSeconds,
			},
		},
		devshardctlEnvOverrides: map[string]string{
			e2econfig.RefusalTimeoutSecondsEnv:   shortProtocolTimeoutSeconds,
			e2econfig.ExecutionTimeoutSecondsEnv: shortProtocolTimeoutSeconds,
		},
	})

	nextSlot := int((testutil.LatestSessionNonce(t, client, env.clientURL) + 1) % uint64(len(env.hostURLs)))
	require.Equal(t, 1, nextSlot, "test setup expects the delayed host to own the first traffic nonce")

	testutil.PostJSON(t, client, env.clientURL+"/v1/admin/settings", map[string]any{
		"redundancy": map[string]any{
			"receipt_timeout_ms":               int64(200),
			"non_stream_response_floor_ms":     int64(300),
			"non_stream_no_content_timeout_ms": int64(800),
			"non_stream_max_attempt_wait_ms":   int64(900),
			"secondary_wait_after_winner_ms":   int64(250),
		},
	})

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeUnfinished := testutil.AccountingUnfinishedTotal(before)

	done := make(chan testutil.RawResponseResult, 1)
	go func() {
		resp, err := testutil.SendCompletionRawE(client, env.clientURL, "accounting live send timeout", testutil.AdminAPIKey)
		done <- testutil.RawResponseResult{Response: resp, Err: err}
	}()

	inFlight := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 && testutil.AccountingInFlightTotal(resp) > 0
	})
	inFlightCount := testutil.AccountingInFlightTotal(inFlight)
	require.Greater(t, inFlightCount, uint64(0), "accounting should expose the live sent nonce as in_flight")
	testutil.RequireAccountingResponseCoherent(t, inFlight, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, inFlight)

	select {
	case result := <-done:
		require.NoError(t, result.Err)
		testutil.LogRawResponse(t, "accounting live send timeout completion", result.Response)
		testutil.RequireOpenAINonStreamingCompletion(t, result.Response)
	case <-time.After(testutil.DefaultRequestTimeout):
		t.Fatal("timed out waiting for timeout-backed completion")
	}

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingInFlightTotal(resp) < inFlightCount &&
			testutil.AccountingUnfinishedTotal(resp) > beforeUnfinished
	})
	require.Less(t, testutil.AccountingInFlightTotal(accounting), inFlightCount, "timed-out live send should leave the in_flight bucket")
	require.Greater(t, testutil.AccountingUnfinishedTotal(accounting), beforeUnfinished, "timed-out live send should become an unfinished disposition")
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
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
