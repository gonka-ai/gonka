package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
)

const (
	receiptDelayedSlot          = 1
	receiptDelayMillis          = "8000"
	slowInferenceDelayMillis    = "3000"
	liveSendStalledSlot         = 2
	stalledInferenceDelayMillis = "600000"
	warmupProbeNonce            = 1
	warmupProbePollInterval     = 250 * time.Millisecond
)

// receiptDelayedStandOptions runs the stand on short protocol deadlines with one host delaying its receipt.
func receiptDelayedStandOptions() e2eEnvOptions {
	return e2eEnvOptions{
		gatewayEnvOverrides: shortDeadlineGateway(),
		mockChainParams:     shortDeadlineChain(),
		hostEnvOverrides: map[int]map[string]string{
			0:                  shortDeadlines(nil),
			receiptDelayedSlot: shortDeadlines(map[string]string{e2econfig.ReceiptDelayMillisEnv: receiptDelayMillis}),
			2:                  shortDeadlines(nil),
		},
	}
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Query accounting before user traffic to capture the initial assigned nonce total.
//  3. Send several successful non-streaming completions through the gateway.
//  4. Wait until accounting shows new assigned nonces and at least one disposition.
//  5. Assert the accounting API response is coherent and every nonce is counted once.
func TestE2E_AccountingHappyPath(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

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
//  3. Send several successful non-streaming completions through the gateway.
//  4. Wait until accounting shows dispositions for the consumed nonces.
//  5. Assert each participant-model record has no residual accounting gap:
//     dispositions plus live/residual buckets equal assigned nonces.
func TestE2E_AccountingNoResidualAfterFinishedTraffic(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

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
//  1. Start the three-host environment with every host publishing protocol finish but empty user content.
//  2. Send one non-streaming completion through the gateway, then a second one to commit queued finish txs.
//  3. Poll accounting until a finished protocol nonce has usage=unknown.
//  4. Assert finished_usage_unknown is counted and every assigned nonce is still counted once.
func TestE2E_AccountingFinishedUsageUnknown(t *testing.T) {
	const emptyCompletionBody = `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":0}}`

	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{
			0: {
				e2econfig.StubInferenceResponseBodyEnv: emptyCompletionBody,
			},
			1: {
				e2econfig.StubInferenceResponseBodyEnv: emptyCompletionBody,
			},
			2: {
				e2econfig.StubInferenceResponseBodyEnv: emptyCompletionBody,
			},
		},
	})

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeUnknown := testutil.AccountingDispositionCount(before, "finished_usage_unknown")
	beforeAssigned := testutil.AccountingAssignedTotal(before)

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "accounting finished usage unknown", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "accounting finished usage unknown completion", resp)

	drain := testutil.SendCompletionRaw(t, client, env.clientURL, "accounting finished usage unknown drain", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "accounting finished usage unknown drain completion", drain)

	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 &&
			testutil.AccountingAssignedTotal(resp) > beforeAssigned &&
			testutil.AccountingDispositionCount(resp, "finished_usage_unknown") > beforeUnknown
	})
	require.Greater(t,
		testutil.AccountingDispositionCount(accounting, "finished_usage_unknown"),
		beforeUnknown,
		"protocol-finished attempt without a recoverable winner should be counted as finished_usage_unknown",
	)
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

// Test flow:
//  1. Start the three-host environment with one host delaying inference.
//  2. Send one non-streaming completion asynchronously so its sent nonce stays live.
//  3. Poll accounting until the sent nonce appears in the in_flight bucket.
//  4. Assert in_flight is a current disposition bucket and accounting still balances.
//  5. Wait for the delayed completion and assert the user request still succeeds.
func TestE2E_AccountingLiveInFlightIsCountedOnce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{
			1: {
				e2econfig.StubInferenceDelayMillisEnv: slowInferenceDelayMillis,
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
//  1. Start the three-host environment on short protocol deadlines with the host of the first traffic nonce stalling after its receipt.
//  2. Wait for the warm-up probe to take nonce 1, so the next nonce lands on the stalling host.
//  3. Send one non-streaming completion and observe its live sent nonce in the stalling host's in_flight bucket.
//  4. Assert the user request still succeeds through another host.
//  5. Poll accounting until the stalling host's timed-out live send has left in_flight and become an unfinished disposition, and assert nonce accounting still balances.
func TestE2E_AccountingLiveSendTimeout(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: shortDeadlineGateway(),
		mockChainParams:     shortDeadlineChain(),
		hostEnvOverrides: map[int]map[string]string{
			0:                   shortDeadlines(nil),
			1:                   shortDeadlines(nil),
			liveSendStalledSlot: shortDeadlines(map[string]string{e2econfig.StubInferenceDelayMillisEnv: stalledInferenceDelayMillis}),
		},
	})

	latestNonce := awaitWarmupProbeNonce(t, client, env.clientURL)
	nextSlot := int((latestNonce + 1) % uint64(len(env.hostURLs)))
	require.Equal(t, liveSendStalledSlot, nextSlot, "test setup expects the stalling host to own the first traffic nonce")

	stalledHost := signerAddress(t, testutil.HostPrivateKeys[liveSendStalledSlot])
	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeRow, _ := testutil.AccountingParticipantRow(before, stalledHost)
	beforeUnfinished := unfinishedNonces(beforeRow)

	done := make(chan testutil.RawResponseResult, 1)
	go func() {
		resp, err := testutil.SendCompletionRawE(client, env.clientURL, "accounting live send timeout", testutil.AdminAPIKey)
		done <- testutil.RawResponseResult{Response: resp, Err: err}
	}()

	inFlight := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		row, found := testutil.AccountingParticipantRow(resp, stalledHost)
		return found && row.InFlight > 0
	})
	inFlightRow, _ := testutil.AccountingParticipantRow(inFlight, stalledHost)
	require.Positive(t, inFlightRow.InFlight, "accounting should expose the stalling host's live sent nonce as in_flight")
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
		row, found := testutil.AccountingParticipantRow(resp, stalledHost)
		return found && row.InFlight == 0 && unfinishedNonces(row) > beforeUnfinished
	})
	row, _ := testutil.AccountingParticipantRow(accounting, stalledHost)
	require.Zero(t, row.InFlight, "the timed-out live send should leave the stalling host's in_flight bucket")
	require.Greater(t, unfinishedNonces(row), beforeUnfinished, "the timed-out live send should become an unfinished disposition")
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

func unfinishedNonces(row testutil.AccountingParticipant) uint64 {
	return row.Dispositions["unfinished_refused"] + row.Dispositions["unfinished_execution"]
}

// awaitWarmupProbeNonce waits until the gateway's warm-up probe has taken its nonce and returns the latest nonce.
func awaitWarmupProbeNonce(t *testing.T, client *http.Client, clientURL string) uint64 {
	t.Helper()
	deadline := time.Now().Add(testutil.DefaultRequestTimeout)
	for time.Now().Before(deadline) {
		if latest := gatewaySessionNonce(t, client, clientURL); latest >= warmupProbeNonce {
			return latest
		}
		time.Sleep(warmupProbePollInterval)
	}
	t.Fatalf("the gateway's warm-up probe took no nonce within %s", testutil.DefaultRequestTimeout)
	return 0
}

// Test flow:
//  1. Start the three-host environment on short protocol deadlines with one host delaying its receipt.
//  2. Send one non-streaming completion and observe the no-receipt nonce in-flight.
//  3. Stop that executor before it can publish receipt or finish, while a fast secondary wins.
//  4. Poll accounting until the delayed host's row holds nothing in flight and its timed-out nonce is unfinished_refused.
//  5. Assert that row carries no unfinished_execution, its timeout applied, and nonce accounting still balances.
func TestE2E_AccountingNoReceiptTimeoutBecomesUnfinishedRefused(t *testing.T) {
	env, client := startGatewayEnv(t, receiptDelayedStandOptions())

	done := make(chan testutil.RawResponseResult, 1)
	go func() {
		resp, err := testutil.SendCompletionRawE(client, env.clientURL, "accounting no-receipt unfinished refused", testutil.AdminAPIKey)
		done <- testutil.RawResponseResult{Response: resp, Err: err}
	}()

	inFlight := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 && testutil.AccountingInFlightTotal(resp) > 0
	})
	inFlightCount := testutil.AccountingInFlightTotal(inFlight)
	require.Greater(t, inFlightCount, uint64(0), "accounting should expose the no-receipt sent nonce as in_flight")
	testutil.RequireAccountingResponseCoherent(t, inFlight, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, inFlight)

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, receiptDelayedSlot)

	select {
	case result := <-done:
		require.NoError(t, result.Err)
		testutil.LogRawResponse(t, "accounting no-receipt unfinished refused completion", result.Response)
		testutil.RequireOpenAINonStreamingCompletion(t, result.Response)
	case <-time.After(testutil.DefaultRequestTimeout):
		t.Fatal("timed out waiting for no-receipt timeout-backed completion")
	}

	delayedHost := signerAddress(t, testutil.HostPrivateKeys[receiptDelayedSlot])
	accounting := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		row, found := testutil.AccountingParticipantRow(resp, delayedHost)
		return found &&
			row.InFlight == 0 &&
			row.Dispositions["unfinished_refused"] > 0 &&
			row.Dispositions["unfinished_execution"] == 0 &&
			row.TimeoutOutcomes["applied"] > 0
	})
	row, _ := testutil.AccountingParticipantRow(accounting, delayedHost)
	require.Zero(t, row.InFlight, "the timed-out no-receipt send should leave the in_flight bucket")
	require.Positive(t, row.Dispositions["unfinished_refused"], "no-receipt timeout should become unfinished_refused")
	require.Zero(t, row.Dispositions["unfinished_execution"], "no-receipt timeout must not become unfinished_execution")
	require.Positive(t, row.TimeoutOutcomes["applied"], "no-receipt timeout should apply at protocol level")
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

// Test flow:
//  1. Start the three-host environment on short protocol deadlines with one host delaying its receipt.
//  2. Send one non-streaming completion, observe the no-receipt nonce in-flight, then stop that executor.
//  3. Let a fast secondary win while timeout voting applies a protocol miss for the stopped send.
//  4. Send one more completion to carry the winner's finish, and poll accounting until timeout_outcome=applied and HostStats.Missed move together.
//  5. Assert the applied timeout cross-check remains clean and nonce accounting still balances.
func TestE2E_AccountingAppliedTimeoutCrossCheck(t *testing.T) {
	env, client := startGatewayEnv(t, receiptDelayedStandOptions())

	before := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0
	})
	beforeApplied := testutil.AccountingTimeoutOutcomeCount(before, "applied")
	beforeMisses := testutil.AccountingProtocolMissTotal(before)

	done := make(chan testutil.RawResponseResult, 1)
	go func() {
		resp, err := testutil.SendCompletionRawE(client, env.clientURL, "accounting applied timeout cross-check", testutil.AdminAPIKey)
		done <- testutil.RawResponseResult{Response: resp, Err: err}
	}()

	inFlight := testutil.WaitAccountingParticipants(t, client, env.statsURL, "model=stub-model", func(resp testutil.AccountingParticipantsResponse) bool {
		return len(resp.Participants) > 0 && testutil.AccountingInFlightTotal(resp) > 0
	})
	require.Greater(t, testutil.AccountingInFlightTotal(inFlight), uint64(0), "accounting should expose the live sent nonce before timeout voting")

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, receiptDelayedSlot)

	select {
	case result := <-done:
		require.NoError(t, result.Err)
		testutil.LogRawResponse(t, "accounting applied timeout cross-check completion", result.Response)
		testutil.RequireOpenAINonStreamingCompletion(t, result.Response)
	case <-time.After(testutil.DefaultRequestTimeout):
		t.Fatal("timed out waiting for timeout-backed completion")
	}

	accounting := gatewayLedgerOnceFinishesLand(t, client, env, func(resp testutil.AccountingParticipantsResponse) bool {
		applied := testutil.AccountingTimeoutOutcomeCount(resp, "applied")
		missed := testutil.AccountingProtocolMissTotal(resp)
		return len(resp.Participants) > 0 &&
			applied > beforeApplied &&
			missed > beforeMisses &&
			applied == missed &&
			testutil.AccountingCrossCheckTimeoutAppliedTotal(resp) == applied &&
			testutil.AccountingCrossCheckHostMissedTotal(resp) == missed
	})
	applied := testutil.AccountingTimeoutOutcomeCount(accounting, "applied")
	missed := testutil.AccountingProtocolMissTotal(accounting)
	require.Greater(t, applied, beforeApplied, "timeout_outcome=applied should increase")
	require.Equal(t, applied, missed, "applied timeout count should match HostStats.Missed")
	require.Equal(t, applied, testutil.AccountingCrossCheckTimeoutAppliedTotal(accounting), "cross-check applied side should match timeout outcome count")
	require.Equal(t, missed, testutil.AccountingCrossCheckHostMissedTotal(accounting), "cross-check missed side should match protocol misses")
	testutil.RequireAccountingResponseCoherent(t, accounting, "stub-model")
	testutil.RequireNonceAccountingBalanced(t, accounting)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Stop one devshard-host container.
//  3. Send a non-streaming completion through the gateway and assert success.
//  4. Query accounting for the current epoch and model.
//  5. Assert accounting remains coherent and every assigned nonce is counted once.
func TestE2E_AccountingOneHostUnavailable(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

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

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send two sequential completions so a finish rides the second answer from the same host.
//  3. Poll the accounting participants route until a row reports a paid cost.
//  4. Assert the row names its participant and model, and that its refund is the reserve it did not spend.
func TestE2E_AccountingReportsCostPerParticipantAndModel(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.SendCompletion(t, client, env.clientURL, "money first")
	testutil.SendCompletion(t, client, env.clientURL, "money second")

	ledger := testutil.WaitAccountingParticipants(t, client, env.statsURL, "",
		func(response testutil.AccountingParticipantsResponse) bool {
			for _, participant := range response.Participants {
				if participant.ActualCost > 0 {
					return true
				}
			}
			return false
		})

	for _, participant := range ledger.Participants {
		if participant.RefundedCost != 0 && participant.ActualCost == 0 {
			t.Errorf("%s refunded %d without paying anything: an open reserve is money held",
				participant.Participant, participant.RefundedCost)
		}
	}

	var reported int
	for _, participant := range ledger.Participants {
		if participant.ActualCost == 0 {
			continue
		}
		reported++
		if participant.ChainCost == 0 {
			t.Errorf("%s paid %d and the chain charged its slot nothing", participant.Participant, participant.ActualCost)
		}
		if participant.RefundedCost > participant.ReservedCost {
			t.Errorf("%s refunded %d, more than the %d it reserved",
				participant.Participant, participant.RefundedCost, participant.ReservedCost)
		}
		if participant.Participant == "" || participant.Model == "" {
			t.Errorf("a row carrying %d reserved names participant %q and model %q",
				participant.ReservedCost, participant.Participant, participant.Model)
		}
		if participant.ActualCost > participant.ReservedCost {
			t.Errorf("%s paid %d against a reserve of %d",
				participant.Participant, participant.ActualCost, participant.ReservedCost)
		}
	}
	if reported == 0 {
		t.Fatalf("no participant row carried a paid cost: %+v", ledger.Participants)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send two sequential completions so a host reports its usage.
//  3. Poll accounting until a row reports both output tokens and the chain's input tokens.
//  4. Assert that row names its participant and model.
func TestE2E_AccountingReportsTokensPerParticipantAndModel(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.SendCompletion(t, client, env.clientURL, "tokens first")
	testutil.SendCompletion(t, client, env.clientURL, "tokens second")

	ledger := testutil.WaitAccountingParticipants(t, client, env.statsURL, "",
		func(response testutil.AccountingParticipantsResponse) bool {
			for _, participant := range response.Participants {
				if participant.OutputTokens > 0 && participant.InputTokens > 0 {
					return true
				}
			}
			return false
		})

	for _, participant := range ledger.Participants {
		if participant.OutputTokens == 0 || participant.InputTokens == 0 {
			continue
		}
		if participant.Participant == "" || participant.Model == "" {
			t.Errorf("a row carrying %d input and %d output tokens names participant %q and model %q",
				participant.InputTokens, participant.OutputTokens, participant.Participant, participant.Model)
		}
		return
	}
	t.Fatalf("no participant row carried both input and output tokens: %+v", ledger.Participants)
}
