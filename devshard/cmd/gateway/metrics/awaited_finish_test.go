package metrics

import (
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
)

func awaitedWinnerRace(losers ...engine.AttemptOutcome) (engine.RaceOutcome, engine.AttemptOutcome) {
	winner := winningAttempt()
	winner.NonceFinished = false
	return engine.RaceOutcome{
		Model:       "qwen",
		InputTokens: 100,
		Decision:    "primary",
		WinnerNonce: winner.Nonce,
		Succeeded:   true,
		Attempts:    append([]engine.AttemptOutcome{winner}, losers...),
	}, winner
}

func withNonceFinished(attempt engine.AttemptOutcome, finished bool) engine.AttemptOutcome {
	attempt.NonceFinished = finished
	return attempt
}

func unavailableLoser() engine.AttemptOutcome {
	return engine.AttemptOutcome{
		Participant: "gonka1loser",
		Nonce:       12,
		Role:        "extra",
		StartReason: "primary_slow",
		SendTime:    at(300 * time.Millisecond),
		Completed:   at(1 * time.Second),
		Terminal:    engine.TerminalUnavailable,
	}
}

// Test flow:
//  1. Build a race recorder and record a successful race whose only attempt is a confirmed winner with its nonce still open.
//  2. Assert the winner is counted as started and the request as a success, with no terminal, attempt-failure or hidden-failure series.
//  3. Record the winner's awaited finish as finished.
//  4. Assert one successful user-visible winner terminal and still no attempt failure or hidden failure.
func TestAnAwaitedWinnerThatFinishesIsCountedAsAWinner(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)
	outcome, winner := awaitedWinnerRace()

	recorder.RecordRace(outcome)
	expectCounter(t, telemetry, "devshard_gateway_attempts_started_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "reason": "primary"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_requests_total",
		labels{"model": "qwen", "outcome": "success", "reason": "none"}, 1)
	expectAbsent(t, telemetry, "devshard_gateway_attempts_terminal_total")
	expectAbsent(t, telemetry, "devshard_gateway_attempt_failures_total")
	expectAbsent(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total")

	recorder.RecordAwaitedFinish(outcome, withNonceFinished(winner, true))
	expectCounter(t, telemetry, "devshard_gateway_attempts_terminal_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "outcome": "success", "visibility": "user_visible_winner"}, 1)
	expectAbsent(t, telemetry, "devshard_gateway_attempt_failures_total")
	expectAbsent(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total")
}

// Test flow:
//  1. Build a race recorder and record a successful race whose only attempt is a confirmed winner with its nonce still open.
//  2. Record the winner's awaited finish as still unfinished.
//  3. Assert one failed_not_finished terminal, one not_finished attempt failure, and one not_finished hidden failure on the request.
func TestAnAwaitedWinnerStillOpenIsCountedAsNotFinished(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)
	outcome, winner := awaitedWinnerRace()

	recorder.RecordRace(outcome)
	recorder.RecordAwaitedFinish(outcome, withNonceFinished(winner, false))

	expectCounter(t, telemetry, "devshard_gateway_attempts_terminal_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "outcome": "failed", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_attempt_failures_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "reason": "not_finished", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total",
		labels{"model": "qwen", "reason": "not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_requests_total",
		labels{"model": "qwen", "outcome": "success", "reason": "none"}, 1)
}

// Test flow:
//  1. Build a race recorder and record a successful race with a confirmed winner whose nonce is still open and a loser that failed with a 503.
//  2. Assert the loser's failure and the request's http_503 hidden failure are counted at race end.
//  3. Record the winner's awaited finish as still unfinished.
//  4. Assert the winner's not_finished terminal and attempt failure are counted, and the request still carries its one http_503 hidden failure.
func TestAnAwaitedWinnerLeavesALoserFailureAsTheRequestsHiddenFailure(t *testing.T) {
	telemetry := New()
	recorder := newTestRaceRecorder(telemetry)
	outcome, winner := awaitedWinnerRace(unavailableLoser())

	recorder.RecordRace(outcome)
	expectCounter(t, telemetry, "devshard_gateway_attempt_failures_total",
		labels{"participant_key": "gonka1loser", "model": "qwen", "role": "extra", "reason": "http_503", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total",
		labels{"model": "qwen", "reason": "http_503"}, 1)

	recorder.RecordAwaitedFinish(outcome, withNonceFinished(winner, false))
	expectCounter(t, telemetry, "devshard_gateway_attempts_terminal_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "outcome": "failed", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_attempt_failures_total",
		labels{"participant_key": "gonka1winner", "model": "qwen", "role": "primary", "reason": "not_finished", "visibility": "failed_not_finished"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total",
		labels{"model": "qwen", "reason": "http_503"}, 1)
	if hidden := family(t, telemetry, "devshard_gateway_user_requests_with_hidden_failure_total"); len(hidden.GetMetric()) != 1 {
		t.Fatalf("hidden-failure series = %v, want only the loser's http_503", hidden.GetMetric())
	}
}
