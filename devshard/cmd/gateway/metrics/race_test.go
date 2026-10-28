package metrics

import (
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
)

var raceStart = time.Unix(1700000000, 0)

func at(offset time.Duration) time.Time { return raceStart.Add(offset) }

// newTestRaceRecorder writes every series at raceStart, well inside the staleness window, so nothing ages out.
func newTestRaceRecorder(telemetry *Metrics) *RaceRecorder {
	return NewRaceRecorder(telemetry, func() time.Time { return raceStart }, func() time.Duration { return time.Hour })
}

func winningAttempt() engine.AttemptOutcome {
	return engine.AttemptOutcome{
		Participant:           "gonka1winner",
		Nonce:                 11,
		Role:                  "primary",
		StartReason:           "primary",
		SendTime:              at(0),
		ReceiptTime:           at(200 * time.Millisecond),
		FirstToken:            at(500 * time.Millisecond),
		FirstContent:          at(600 * time.Millisecond),
		Completed:             at(2 * time.Second),
		ContentChunks:         4,
		Terminal:              engine.TerminalWon,
		Confirmed:             true,
		NonceFinished:         true,
		UsageCompletionTokens: 40,
	}
}

// Test flow:
//  1. Build a race recorder and record a successful race with one winning attempt (`winningAttempt`).
//  2. Assert the started, terminal, output-token, and request counters report the winner's values.
//  3. Assert no failure, hidden-failure, or missed-deadline series exist.
//  4. Assert the receipt, first-content, prefill, and total-attempt histograms report the winner's timings.
func TestAWonRaceEmitsTheWinnerFamiliesWithTheirValues(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)

	recorder.RecordRace(engine.RaceOutcome{
		Model:       "qwen",
		InputTokens: 100,
		Decision:    "primary",
		WinnerNonce: 11,
		Succeeded:   true,
		Attempts:    []engine.AttemptOutcome{winningAttempt()},
	})

	expectCounter(t, telemetry, "devshard_gateway_attempts_started_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "reason": "primary"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_attempts_terminal_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "outcome": "success", "visibility": "user_visible_winner"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_participant_output_tokens_total",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 40)
	expectCounter(t, telemetry, "devshard_gateway_requests_total",
		labels{"model": "qwen", "outcome": "success", "reason": "none"}, 1)
	expectAbsent(t, telemetry, "devshard_gateway_attempt_failures_total")
	expectAbsent(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total")
	expectAbsent(t, telemetry, "devshard_gateway_participant_missed_deadlines_total")

	expectHistogram(t, telemetry, "devshard_gateway_participant_receipt_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 0.2)
	expectHistogram(t, telemetry, "devshard_gateway_participant_first_content_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 0.6)
	expectHistogram(t, telemetry, "devshard_gateway_participant_prefill_seconds_per_input_token",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 0.004)
	expectHistogram(t, telemetry, "devshard_gateway_participant_total_attempt_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 2)
}

// Test flow:
//  1. Build a race recorder and record a winning attempt whose `FirstContent` was never set (a role-only chunk, not content).
//  2. Assert the first-content and prefill-per-input-token histograms are absent.
//  3. Assert the receipt histogram still reports the winner's receipt time.
func TestAContentlessStreamLeavesTheContentLatenciesUnobserved(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)

	attempt := winningAttempt()
	attempt.FirstContent = time.Time{}
	recorder.RecordRace(engine.RaceOutcome{
		Model: "qwen", InputTokens: 100, Decision: "primary", WinnerNonce: 11,
		Succeeded: true, Attempts: []engine.AttemptOutcome{attempt},
	})

	expectAbsent(t, telemetry, "devshard_gateway_participant_first_content_seconds")
	expectAbsent(t, telemetry, "devshard_gateway_participant_prefill_seconds_per_input_token")
	expectHistogram(t, telemetry, "devshard_gateway_participant_receipt_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 0.2)
}

// Test flow:
//  1. Build a race recorder and record a winning attempt with a long max chunk gap.
//  2. Assert the max-inter-chunk-seconds histogram reports the longest silence and the inter-chunk histogram reports the mean gap.
func TestAStalledAttemptReportsItsLongestSilence(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)

	attempt := winningAttempt()
	attempt.MaxChunkGap, attempt.MeanChunkGap = 55*time.Second, 40*time.Millisecond
	recorder.RecordRace(engine.RaceOutcome{
		Model: "qwen", InputTokens: 100, Decision: "primary", WinnerNonce: 11,
		Succeeded: true, Attempts: []engine.AttemptOutcome{attempt},
	})

	expectHistogram(t, telemetry, "devshard_gateway_participant_max_inter_chunk_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 55)
	expectHistogram(t, telemetry, "devshard_gateway_participant_inter_chunk_seconds",
		labels{"participant_key": "gonka1winner", "model": "qwen"}, 1, 0.04)
}

// Test flow:
//  1. Build a race recorder and record a race with a winning attempt plus a losing attempt that failed with a 503.
//  2. Assert the loser's attempt-failure and transport-error counters, and the request's hidden-failure counter, all report the failure.
//  3. Assert the loser's output tokens are still counted and the request is still counted as an overall success.
func TestAHiddenLoserFailureIsCountedAgainstASuccessfulRequest(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)

	recorder.RecordRace(engine.RaceOutcome{
		Model:       "qwen",
		InputTokens: 100,
		Decision:    "primary_slow",
		WinnerNonce: 11,
		Succeeded:   true,
		Attempts: []engine.AttemptOutcome{
			winningAttempt(),
			{
				Participant:           "gonka1loser",
				Nonce:                 12,
				Role:                  "extra",
				StartReason:           "primary_slow",
				SendTime:              at(300 * time.Millisecond),
				Completed:             at(1 * time.Second),
				Terminal:              engine.TerminalUnavailable,
				UsageCompletionTokens: 12,
			},
		},
	})

	expectCounter(t, telemetry, "devshard_gateway_attempt_failures_total",
		labels{"participant_key": "gonka1loser", "model": "qwen", "role": "extra", "reason": "http_503", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_participant_transport_errors_total",
		labels{"participant_key": "gonka1loser", "model": "qwen", "status": "503"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total",
		labels{"model": "qwen", "reason": "http_503"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_participant_output_tokens_total",
		labels{"participant_key": "gonka1loser", "model": "qwen"}, 12)
	expectCounter(t, telemetry, "devshard_gateway_requests_total",
		labels{"model": "qwen", "outcome": "success", "reason": "none"}, 1)
}

// Test flow:
//  1. Build a race recorder and record a race whose only attempt is marked suspicious with no winner.
//  2. Assert the attempt-failure counter reports it with visibility "no_winner" and the request counter reports an overall failure.
//  3. Assert no transport-error series exists.
func TestASuspiciousAttemptIsCountedAsNoWinner(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)

	recorder.RecordRace(engine.RaceOutcome{
		Model:    "qwen",
		Decision: "primary",
		Attempts: []engine.AttemptOutcome{{
			Participant:   "gonka1quiet",
			Nonce:         21,
			Role:          "primary",
			SendTime:      at(0),
			Completed:     at(time.Second),
			Suspicious:    true,
			Terminal:      engine.TerminalEmptyStream,
			Confirmed:     true,
			NonceFinished: true,
		}},
	})

	expectCounter(t, telemetry, "devshard_gateway_attempt_failures_total",
		labels{"participant_key": "gonka1quiet", "model": "qwen", "role": "primary", "reason": "empty_stream", "visibility": "no_winner"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_requests_total",
		labels{"model": "qwen", "outcome": "failure", "reason": "empty_stream"}, 1)
	expectAbsent(t, telemetry, "devshard_gateway_participant_transport_errors_total")
}

// Test flow:
//  1. Table-driven: each case builds a `RaceOutcome` with a distinct lifecycle state (no attempts, escrow missing, balance exhausted, client gone before any attempt, client gone with a losing attempt) and the failure reason it must report.
//  2. For each case, record the race.
//  3. Assert the request counter reports a failure under the case's expected reason.
func TestALifecycleFailureNamesItselfRatherThanAnAttempt(t *testing.T) {
	testCases := []struct {
		name     string
		outcome  engine.RaceOutcome
		expected string
	}{
		{
			name:     "no attempt was ever dispatched",
			outcome:  engine.RaceOutcome{Model: "qwen", Decision: "primary"},
			expected: "no_attempts",
		},
		{
			name:     "the escrow disappeared",
			outcome:  engine.RaceOutcome{Model: "qwen", Decision: "primary", Lifecycle: engine.Lifecycle{EscrowMissing: true}},
			expected: "escrow_missing",
		},
		{
			name:     "the escrow ran out of funds",
			outcome:  engine.RaceOutcome{Model: "qwen", Decision: "primary", Lifecycle: engine.Lifecycle{BalanceExhausted: true}},
			expected: "balance_exhausted",
		},
		{
			name:     "the client left before any attempt",
			outcome:  engine.RaceOutcome{Model: "qwen", Decision: "primary", Lifecycle: engine.Lifecycle{ClientGone: true}},
			expected: "client_cancelled",
		},
		{
			name: "the client left and no attempt won",
			outcome: engine.RaceOutcome{
				Model:     "qwen",
				Decision:  "primary",
				Lifecycle: engine.Lifecycle{ClientGone: true},
				Attempts: []engine.AttemptOutcome{{
					Participant: "gonka1loser",
					Nonce:       12,
					Role:        "primary",
					StartReason: "primary",
					SendTime:    at(0),
					Terminal:    engine.TerminalErrorStream,
				}},
			},
			expected: "client_cancelled",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			telemetry := New()
			newTestRaceRecorder(telemetry).RecordRace(testCase.outcome)

			expectCounter(t, telemetry, "devshard_gateway_requests_total",
				labels{"model": "qwen", "outcome": "failure", "reason": testCase.expected}, 1)
		})
	}
}

// Test flow:
//  1. Build a race recorder and record a race whose only attempt never dispatched (off-path, no `SendTime`).
//  2. Assert no attempts-started series exists.
//  3. Assert the terminal counter still reports the attempt as failed and not finished.
func TestAnUndispatchedAttemptIsNotCountedAsStarted(t *testing.T) {
	telemetry := New()
	newTestRaceRecorder(telemetry).RecordRace(engine.RaceOutcome{
		Model:    "qwen",
		Decision: "primary",
		Attempts: []engine.AttemptOutcome{{Participant: "gonka1ghost", Role: "extra", Terminal: engine.TerminalOffPath}},
	})

	expectAbsent(t, telemetry, "devshard_gateway_attempts_started_total")
	expectCounter(t, telemetry, "devshard_gateway_attempts_terminal_total",
		labels{"participant_key": "gonka1ghost", "model": "qwen", "role": "extra", "outcome": "failed", "visibility": "failed_not_finished"}, 1)
}

// Test flow:
//  1. Build a race recorder and record a race whose attempt failed with an upstream server error at status 502.
//  2. Assert the transport-error counter reports it under status "502".
func TestAnUpstreamServerErrorIsCountedUnderTheStatusTheHostSent(t *testing.T) {
	telemetry := New()
	newTestRaceRecorder(telemetry).RecordRace(engine.RaceOutcome{
		Model:    "qwen",
		Decision: "primary",
		Attempts: []engine.AttemptOutcome{{
			Participant: "gonka1host", Role: "primary", SendTime: at(0),
			Terminal: engine.TerminalUpstreamServerError, UpstreamStatus: 502,
		}},
	})

	expectCounter(t, telemetry, "devshard_gateway_participant_transport_errors_total",
		labels{"participant_key": "gonka1host", "model": "qwen", "status": "502"}, 1)
}

// Test flow:
//  1. Build a race recorder and record a race with no model and an attempt with no participant.
//  2. Assert the started and transport-error counters fall back to "unknown" labels rather than blank ones.
func TestEmptyLabelsFallBackRatherThanShippingBlank(t *testing.T) {
	telemetry := New()
	newTestRaceRecorder(telemetry).RecordRace(engine.RaceOutcome{
		Attempts: []engine.AttemptOutcome{{SendTime: at(0), Terminal: engine.TerminalDialFailure}},
	})

	expectCounter(t, telemetry, "devshard_gateway_attempts_started_total",
		labels{"participant_key": "unknown", "model": "unknown", "role": "primary", "reason": "primary"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_participant_transport_errors_total",
		labels{"participant_key": "unknown", "model": "unknown", "status": "0"}, 1)
}

// Test flow:
//  1. Table-driven: each case is a `TimeoutEvent` with a distinct action (posted, failed, never attempted).
//  2. For each case, record the timeout.
//  3. Assert the timeout-actions counter reports it under the event's own kind, action, and reason.
func TestATimeoutVoteIsCountedWhateverItsAction(t *testing.T) {
	testCases := []struct {
		name  string
		event engine.TimeoutEvent
	}{
		{
			name:  "a posted vote",
			event: engine.TimeoutEvent{Participant: "gonka1host", Model: "qwen", Kind: "refused", Action: "completed", Reason: "refused"},
		},
		{
			name:  "a failed vote",
			event: engine.TimeoutEvent{Participant: "gonka1host", Model: "qwen", Kind: "execution", Action: "failed", Reason: "timeout_collection_error"},
		},
		{
			name:  "a vote that was never attempted",
			event: engine.TimeoutEvent{Participant: "gonka1host", Model: "qwen", Kind: "refused", Action: "skipped", Reason: "nonce_already_finished"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			telemetry := New()
			newTestRaceRecorder(telemetry).RecordTimeout(testCase.event)

			expectCounter(t, telemetry, "devshard_gateway_timeout_actions_total", labels{
				"participant_key": testCase.event.Participant,
				"model":           testCase.event.Model,
				"kind":            testCase.event.Kind,
				"action":          testCase.event.Action,
				"reason":          testCase.event.Reason,
			}, 1)
		})
	}
}

// Test flow:
//  1. Build a race recorder and record a winning attempt that missed both its receipt and first-token deadlines.
//  2. Assert the missed-deadlines counter reports both deadlines under the winner's own name, even though the attempt went on to win.
func TestAMissedDeadlineIsCountedAgainstTheHostThatMissedIt(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)
	late := winningAttempt()
	late.ReceiptDeadlineMissed = true
	late.FirstTokenDeadlineMissed = true

	recorder.RecordRace(engine.RaceOutcome{
		Model: "qwen", InputTokens: 100, Decision: "primary", WinnerNonce: 11,
		Succeeded: true, Attempts: []engine.AttemptOutcome{late},
	})

	expectCounter(t, telemetry, "devshard_gateway_participant_missed_deadlines_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "deadline": "receipt_timeout"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_participant_missed_deadlines_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "deadline": "first_token_timeout"}, 1)
}

// Test flow:
//  1. Build a race recorder and record a classify overflow for one host.
//  2. Assert the stream-carry-overflow counter reports it against that host.
func TestAClassifyOverflowIsAttributedToItsHost(t *testing.T) {
	telemetry := New()
	newTestRaceRecorder(telemetry).RecordClassifyOverflow("gonka1host", "qwen")

	expectCounter(t, telemetry, "devshard_gateway_stream_carry_overflow_total",
		labels{"participant_key": "gonka1host", "model": "qwen"}, 1)
}

// Test flow:
//  1. Build a race recorder and drive a won race, a suspicious no-winner race, and a timeout vote.
//  2. Assert none of the retired metric families (no-winner attempts, user-visible wins, critical user failures, escalation decisions, inference timeouts) were published.
func TestTheRecorderPublishesNoRemovedFamily(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)
	quiet := engine.AttemptOutcome{
		Participant: "gonka1quiet", Nonce: 21, Role: "primary", SendTime: at(0), Completed: at(time.Second),
		Suspicious: true, Terminal: engine.TerminalEmptyStream, Confirmed: true, NonceFinished: true,
	}

	recorder.RecordRace(engine.RaceOutcome{
		Model: "qwen", InputTokens: 100, Decision: "primary", WinnerNonce: 11,
		Succeeded: true, Attempts: []engine.AttemptOutcome{winningAttempt()},
	})
	recorder.RecordRace(engine.RaceOutcome{Model: "qwen", Decision: "primary", Attempts: []engine.AttemptOutcome{quiet}})
	recorder.RecordTimeout(engine.TimeoutEvent{Participant: "gonka1host", Model: "qwen", Kind: "refused", Action: "completed", Reason: "refused"})

	expectAbsent(t, telemetry, "devshard_gateway_no_winner_attempts_total")
	expectAbsent(t, telemetry, "devshard_gateway_user_visible_wins_total")
	expectAbsent(t, telemetry, "devshard_gateway_critical_user_failures_total")
	expectAbsent(t, telemetry, "devshard_gateway_escalation_decisions_total")
	expectAbsent(t, telemetry, "devshard_inference_timeouts_total")
}

// The engine's hook is satisfied structurally; a signature drift must fail the build, not a scrape.
var _ interface {
	RecordRace(engine.RaceOutcome)
	RecordTimeout(engine.TimeoutEvent)
	RecordClassifyOverflow(participant, model string)
} = (*RaceRecorder)(nil)

// Test flow:
//  1. Build a table pairing every engine label constant with the literal wire string a dashboard reads.
//  2. Assert each constant's value still equals its pinned wire string.
func TestEmittedLabelValuesMatchTheirWireStrings(t *testing.T) {
	pinned := []struct{ emitted, want string }{
		{engine.AttemptOutcomeSuccess, "success"},
		{engine.AttemptOutcomeFailed, "failed"},
		{engine.VisibilityWinner, "user_visible_winner"},
		{engine.VisibilityNoWinner, "no_winner"},
		{engine.VisibilitySuppressedLoser, "suppressed_loser"},
		{engine.VisibilityFailedNotFinished, "failed_not_finished"},
		{engine.RolePrimary, "primary"},
		{engine.RoleSpeculative, "speculative"},
		{engine.TimeoutActionSkipped, "skipped"},
		{engine.TimeoutActionStarted, "started"},
		{engine.TimeoutActionCompleted, "completed"},
		{engine.TimeoutActionFailed, "failed"},
		{outcomeFailure, "failure"},
	}
	for _, label := range pinned {
		if label.emitted != label.want {
			t.Errorf("label value %q no longer matches the string dashboards read, %q", label.emitted, label.want)
		}
	}
}
