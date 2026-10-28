package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
	"devshard/host"
	"devshard/user"
)

var errVoteCause = errors.New("collect timeout votes")

// scriptedTimeoutHandler reproduces Session.HandleTimeout's four returns verbatim.
type scriptedTimeoutHandler struct {
	result    user.TimeoutResult
	err       error
	calls     int
	missCalls int
	finishTx  []byte
}

func (h *scriptedTimeoutHandler) TimeoutDeadline(uint64, time.Time) (string, time.Time) {
	return TimeoutKindExecution, time.Time{}
}

func (h *scriptedTimeoutHandler) FinishTxFor(uint64) []byte { return h.finishTx }

func (h *scriptedTimeoutHandler) HandleErrorMiss(context.Context, uint64, []byte, []byte) (user.TimeoutResult, error) {
	h.missCalls++
	return h.result, h.err
}

func (h *scriptedTimeoutHandler) HandleTimeout(context.Context, uint64, time.Time, *host.InferencePayload) (user.TimeoutResult, error) {
	h.calls++
	return h.result, h.err
}

// Test flow:
//  1. For each table case's handler result and error (a vote that reached the escrow state, one shaped like a settled vote but unapplied, votes that sufficed but landed no timeout, insufficient votes, a diff-send failure, a vote-collection failure, a named verifier failure, a deadline never reached), settle a timeout step through a `SessionTimeouts` wrapping a `scriptedTimeoutHandler` scripted with that result and error.
//  2. Assert the returned vote's kind and detail match the case's expectation.
//  3. Assert whether the call reports failure matches the case's expectation, driven only by the handler's own `Applied` flag rather than by whether an error came back.
//  4. Assert the handler was called exactly once.
func TestSessionTimeoutsReadsAPostedVoteThroughTheHandlersAppliedFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		result     user.TimeoutResult
		err        error
		wantVote   string
		wantDetail string
		wantFailed bool
	}{
		{
			name:     "vote_reached_the_escrow_state",
			result:   user.TimeoutResult{Reason: "execution", Applied: true},
			err:      fmt.Errorf("inference %d timed out: %s", 7, "execution"),
			wantVote: "execution",
		},
		{
			name:       "unsettled_but_shaped_like_a_settled_vote",
			result:     user.TimeoutResult{Reason: "execution"},
			err:        fmt.Errorf("inference %d timed out: %s", 7, "execution"),
			wantVote:   "execution",
			wantFailed: true,
		},
		{
			name:       "votes_sufficed_but_the_diff_landed_no_timeout",
			result:     user.TimeoutResult{Reason: "execution"},
			err:        fmt.Errorf("inference %d: %w: diff landed no timeout", 7, user.ErrTimeoutNotApplied),
			wantVote:   "execution",
			wantFailed: true,
		},
		{
			name:       "insufficient_votes_reads_as_a_failure",
			result:     user.TimeoutResult{Reason: "refused"},
			err:        fmt.Errorf("inference %d: %w: insufficient votes", 7, user.ErrTimeoutNotApplied),
			wantVote:   "refused",
			wantFailed: true,
		},
		{
			name:       "diff_send_failure_wraps_its_cause",
			result:     user.TimeoutResult{Reason: "execution"},
			err:        fmt.Errorf("send timeout diff: %w", errVoteCause),
			wantVote:   "execution",
			wantFailed: true,
		},
		{
			name:       "vote_collection_failure_wraps_its_cause",
			result:     user.TimeoutResult{Reason: "refused"},
			err:        fmt.Errorf("collect timeout votes: %w", errVoteCause),
			wantVote:   "refused",
			wantFailed: true,
		},
		{
			name:       "the verifier failure the handler named travels with the vote",
			result:     user.TimeoutResult{Reason: "refused", DetailReason: "verifier_unreachable"},
			err:        fmt.Errorf("collect timeout votes: %w", errVoteCause),
			wantVote:   "refused",
			wantDetail: "verifier_unreachable",
			wantFailed: true,
		},
		{
			name:       "deadline_never_reached_reports_no_reason_and_fails",
			result:     user.TimeoutResult{},
			err:        context.Canceled,
			wantFailed: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handler := &scriptedTimeoutHandler{result: testCase.result, err: testCase.err}
			poster := &SessionTimeouts{handler: handler}

			vote, err := poster.SettleTimeout(context.Background(), TimeoutStep{Nonce: 7, StartedAt: testEpoch})

			if vote.Detail != testCase.wantDetail {
				t.Errorf("vote detail = %q, want %q", vote.Detail, testCase.wantDetail)
			}
			if vote.Kind != testCase.wantVote {
				t.Errorf("vote kind = %q, want %q", vote.Kind, testCase.wantVote)
			}
			if (err != nil) != testCase.wantFailed {
				t.Errorf("err = %v, want failed = %v", err, testCase.wantFailed)
			}
			if handler.calls != 1 {
				t.Errorf("handler calls = %d, want 1", handler.calls)
			}
		})
	}
}

// Test flow:
//  1. Build a `NewSessionTimeouts` from a session and a payload.
//  2. Assert its handler is the session itself and its payload is the one it was given.
func TestNewSessionTimeoutsWiresTheSessionAndItsPayload(t *testing.T) {
	t.Parallel()
	session := &user.Session{}
	payload := &host.InferencePayload{}

	poster := NewSessionTimeouts(session, payload)

	if poster.handler != timeoutHandler(session) {
		t.Errorf("handler = %v, want the session itself", poster.handler)
	}
	if poster.payload != payload {
		t.Errorf("payload = %v, want the request's own", poster.payload)
	}
}

// Test flow:
//  1. Settle a race with a failed attempt through a `SessionTimeouts` wrapping a handler scripted with an applied execution result.
//  2. Assert two events were reported.
//  3. Assert the completed event's action and kind reflect the applied execution vote.
func TestSettleTimeoutsRecordsAPostedVoteAsCompleted(t *testing.T) {
	t.Parallel()
	handler := &scriptedTimeoutHandler{
		result: user.TimeoutResult{Reason: "execution", Applied: true},
		err:    fmt.Errorf("inference %d timed out: %s", 7, "execution"),
	}
	outcome := race(failedAttempt(TerminalDialFailure))

	events := settleEvents(outcome, &SessionTimeouts{handler: handler})

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[1].Action != TimeoutActionCompleted {
		t.Errorf("action = %q, want %q", events[1].Action, TimeoutActionCompleted)
	}
	if events[1].Kind != TimeoutKindExecution {
		t.Errorf("kind = %q, want execution", events[1].Kind)
	}
}

// Test flow:
//  1. Settle a race through a `SessionTimeouts` wrapping a handler whose error reports the diff landed no timeout.
//  2. Assert the final event's action is failed with the not-applied reason.
func TestSettleTimeoutsNamesADiffThatCarriedNoTimeout(t *testing.T) {
	t.Parallel()
	handler := &scriptedTimeoutHandler{
		result: user.TimeoutResult{Reason: "execution"},
		err:    fmt.Errorf("inference %d: %w: diff landed no timeout", 7, user.ErrTimeoutNotApplied),
	}

	events := settleEvents(race(failedAttempt(TerminalDialFailure)), &SessionTimeouts{handler: handler})

	settled := events[len(events)-1]
	if settled.Action != TimeoutActionFailed {
		t.Fatalf("action = %q, want %q", settled.Action, TimeoutActionFailed)
	}
	if settled.Reason != TimeoutReasonNotApplied {
		t.Fatalf("reason = %q, want %q", settled.Reason, TimeoutReasonNotApplied)
	}
}

// Test flow:
//  1. Run a simulated race whose one host refuses.
//  2. Assert the run fails, drain the reported outcome, and settle it.
//  3. Assert both the routing pick and the settlement poster saw the caller's own params value.
func TestRunHandsTheCallersParamsToRoutingAndToSettlement(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, err: errors.New("host refused")})
	want := sim.profile().Params

	if _, err := sim.run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the failed attempt's error")
	}
	sim.reported(t)
	sim.settleAll()

	routed := sim.picker.profiles
	if len(routed) != 1 || routed[0].Params != want {
		t.Fatalf("routing saw %d picks carrying %#v, want one carrying %#v", len(routed), routed, want)
	}
	settled := sim.poster.paramsSeen()
	if len(settled) != 1 || settled[0] != want {
		t.Fatalf("settlement saw params %#v, want one %#v", settled, want)
	}
}

// Test flow:
//  1. Push a primary host's failure streak to the pool-wide ejection cap so the health tracker would eject it, while confirming routing itself still leaves it eligible.
//  2. Run a race where the ejected-but-routable primary refuses and a second host answers as a hedge.
//  3. Assert the race succeeds, its decision is `StartPrimaryDegraded`, and both the primary and its hedge are recorded as attempts.
func TestRunHedgesAPrimaryTheDetectorWantedOutOfRotation(t *testing.T) {
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	settings := config.Defaults()
	sim.perf.health = perf.NewTracker(config.NewHolder(&settings), sim.clock.Now)
	for range settings.Perf.ConsecutiveFailThreshold {
		for _, participant := range []string{"host-0", "host-1"} {
			sim.perf.health.RecordSample(perf.Sample{ParticipantKey: participant, Model: qwenModel})
		}
	}
	if sim.perf.health.Ejected("host-1", qwenModel) {
		t.Fatal("host-1 was withheld from routing, so the routing gate would already have covered it")
	}
	sim.host(10, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("hedged")},
		confirmed: true,
		finished:  true,
	})
	sim.host(11, 0, "host-0", &hostScript{receipt: true, err: errors.New("host refused")})

	if _, err := sim.run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	reported := sim.reported(t)
	sim.settleAll()

	if reported.Decision != StartPrimaryDegraded {
		t.Fatalf("decision = %q, want %q", reported.Decision, StartPrimaryDegraded)
	}
	if len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d, want the primary and the hedge it earned", len(reported.Attempts))
	}
}

// Test flow:
//  1. Run a simulated race whose one host refuses, drain the reported outcome, and settle it.
//  2. Assert the ledger receives exactly one accounting row matching the reported outcome's request, winner nonce and attempt count.
//  3. Assert no extra row was accounted.
func TestTheRecordingPointAccountsEveryRaceExactlyOnce(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, err: errors.New("host refused")})

	if _, err := sim.run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the failed attempt's error")
	}
	reported := sim.reported(t)
	sim.settleAll()

	select {
	case accounted := <-sim.ledger.rows:
		if accounted.RequestID != reported.RequestID || accounted.WinnerNonce != reported.WinnerNonce {
			t.Fatalf("ledger saw %+v, want the reported outcome %+v", accounted, reported)
		}
		if len(accounted.Attempts) != len(reported.Attempts) {
			t.Fatalf("ledger saw %d attempts, want %d", len(accounted.Attempts), len(reported.Attempts))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the race was never accounted")
	}
	if extra := len(sim.ledger.rows); extra != 0 {
		t.Fatalf("rows accounted = %d, want 1", 1+extra)
	}
}

// Test flow:
//  1. Observe one fewer than `crownDenialStrikes` contentless answers for a participant/model and assert crowning is not yet denied.
//  2. Observe one more contentless answer, crossing the threshold, and assert crowning is now denied for that model but not for another model.
//  3. Observe a content-bearing answer and assert crowning is restored.
func TestCrownStrikesDenyOnlyAfterRepeatedContentlessAnswers(t *testing.T) {
	t.Parallel()
	gate := newCrownStrikes(nil)

	for range crownDenialStrikes - 1 {
		gate.Observe(testParticipant, testModel, true)
		if gate.Denied(testParticipant, testModel) {
			t.Fatal("crowning denied before the strike threshold")
		}
	}
	gate.Observe(testParticipant, testModel, true)

	if !gate.Denied(testParticipant, testModel) {
		t.Fatal("crowning still allowed at the strike threshold")
	}
	if gate.Denied(testParticipant, "another-model") {
		t.Error("a strike on one model denied crowning on another")
	}

	gate.Observe(testParticipant, testModel, false)

	if gate.Denied(testParticipant, testModel) {
		t.Error("a content-bearing answer did not restore crowning")
	}
}

// Test flow:
//  1. For each table case's race outcome (no attempt started, the winner breaking after streaming with and without a named reason, a host's own refusal, a trusted or suspicious context-length rejection competing with an earlier refusal, the crowned attempt's refusal outranking a rejection, an empty stream, every attempt taken by proof of compute, an unexplained failure, hosts refusing as unavailable/throttled/mixed, one non-availability failure among unavailable ones, a trusted host error over an all-unavailable race), compute `outcome.failure()`.
//  2. Assert a `HostApplicationError` case matches by type and message, and any other case matches the expected sentinel via `errors.Is`.
func TestOutcomeFailureNamesWhatTheClientLost(t *testing.T) {
	t.Parallel()
	streamedThenFailed := failedAttempt(TerminalUnexpectedEOF)
	streamedThenFailed.ContentChunks = 4

	streamedThenNamedWhy := failedAttempt(TerminalErrorStream)
	streamedThenNamedWhy.ContentChunks = 4
	streamedThenNamedWhy.ErrorSource = "error.server_error"
	streamedThenNamedWhy.ErrorType = "server_error"
	streamedThenNamedWhy.ErrorMessage = "backend exploded"

	hostRefused := failedAttempt(TerminalErrorStream)
	hostRefused.ErrorSource = "error.BadRequestError"
	hostRefused.ErrorType = "BadRequestError"
	hostRefused.ErrorMessage = "model does not exist"

	contextRejected := failedAttempt(TerminalCapabilityRefused)
	contextRejected.Nonce = hostRefused.Nonce + 1
	contextRejected.ErrorSource = "error.BadRequestError"
	contextRejected.ErrorType = "BadRequestError"
	contextRejected.ErrorMessage = vllmContextTotalMessage

	suspiciousContextRejected := contextRejected
	suspiciousContextRejected.Suspicious = true

	throttledWithReason := failedAttempt(TerminalThrottled)
	throttledWithReason.ErrorSource = "error.RateLimitError"
	throttledWithReason.ErrorType = "RateLimitError"
	throttledWithReason.ErrorMessage = "rate limited"

	shorterHostRefused := failedAttempt(TerminalCapabilityRefused)
	shorterHostRefused.Nonce = 11
	shorterHostRefused.ErrorSource = "error.BadRequestError"
	shorterHostRefused.ErrorType = "BadRequestError"
	shorterHostRefused.ErrorMessage = contextRejection(180_000, 200_000)

	longerHostRefused := shorterHostRefused
	longerHostRefused.Nonce = 12
	longerHostRefused.ErrorMessage = contextRejection(262_144, 200_000)

	fullHostBroke := failedAttempt(TerminalErrorStream)
	fullHostBroke.Nonce = 13
	fullHostBroke.ErrorSource = "error.server_error"
	fullHostBroke.ErrorType = "server_error"
	fullHostBroke.ErrorMessage = "backend failed"

	fullHostSilent := failedAttempt(TerminalDialFailure)
	fullHostSilent.Nonce = 14

	const modelContextLength = 400_000

	cases := []struct {
		name    string
		outcome RaceOutcome
		want    error
	}{
		{name: "a_served_race_has_no_failure", outcome: race(cleanAttempt())},
		{
			name:    "a_full_length_hosts_own_error_outranks_a_shorter_hosts_context_refusal",
			outcome: RaceOutcome{Model: testModel, ModelContextLength: modelContextLength, Attempts: []AttemptOutcome{shorterHostRefused, fullHostBroke}},
			want:    &HostApplicationError{Type: "server_error", Message: "backend failed"},
		},
		{
			name:    "a_shorter_hosts_context_refusal_is_not_the_answer_when_another_host_failed_unexplained",
			outcome: RaceOutcome{Model: testModel, ModelContextLength: modelContextLength, Attempts: []AttemptOutcome{shorterHostRefused, fullHostSilent}},
			want:    ErrAllAttemptsFailed,
		},
		{
			name:    "when_every_host_refused_the_context_the_longest_limit_is_the_answer",
			outcome: RaceOutcome{Model: testModel, ModelContextLength: modelContextLength, Attempts: []AttemptOutcome{shorterHostRefused, longerHostRefused}},
			want:    &HostApplicationError{Type: "BadRequestError", Message: contextRejection(262_144, 200_000)},
		},
		{
			name:    "no_attempt_ever_started",
			outcome: RaceOutcome{Model: testModel},
			want:    ErrAllAttemptsFailed,
		},
		{
			name:    "the_winner_broke_after_streaming",
			outcome: failedRace(streamedThenFailed),
			want:    ErrWinnerIncomplete,
		},
		{
			name:    "the_winner_broke_after_streaming_and_named_why",
			outcome: failedRace(streamedThenNamedWhy),
			want:    &HostApplicationError{Type: "server_error", Message: "backend exploded"},
		},
		{
			name:    "the_host_refused_in_words_the_client_must_see",
			outcome: failedRace(hostRefused),
			want:    &HostApplicationError{Type: "BadRequestError", Message: "model does not exist"},
		},
		{
			name:    "a_trusted_context_length_rejection_outranks_an_earlier_refusal",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{hostRefused, contextRejected}},
			want:    &HostApplicationError{Type: "BadRequestError", Message: vllmContextTotalMessage},
		},
		{
			name:    "a_suspicious_hosts_context_length_rejection_outranks_nothing",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{hostRefused, suspiciousContextRejected}},
			want:    &HostApplicationError{Type: "BadRequestError", Message: "model does not exist"},
		},
		{
			name:    "the_crowned_attempts_refusal_outranks_a_context_length_rejection",
			outcome: RaceOutcome{Model: testModel, WinnerNonce: hostRefused.Nonce, Attempts: []AttemptOutcome{contextRejected, hostRefused}},
			want:    &HostApplicationError{Type: "BadRequestError", Message: "model does not exist"},
		},
		{
			name:    "every_host_answered_with_nothing",
			outcome: failedRace(failedAttempt(TerminalEmptyStream)),
			want:    ErrEmptyStream,
		},
		{
			name:    "proof_of_compute_took_every_host_the_request_reached",
			outcome: failedRace(phaseAbortedAttempt(failedAttempt(TerminalEmptyStream))),
			want:    ErrPoCTookTheHosts,
		},
		{
			name:    "nothing_upstream_explained_itself",
			outcome: failedRace(failedAttempt(TerminalDialFailure)),
			want:    ErrAllAttemptsFailed,
		},
		{
			name: "every_host_refused_as_unavailable",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{
				failedAttempt(TerminalUnavailable), failedAttempt(TerminalUnavailable),
			}},
			want: ErrHostsUnavailable,
		},
		{
			name: "every_host_refused_as_throttled",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{
				failedAttempt(TerminalThrottled), failedAttempt(TerminalThrottled),
			}},
			want: ErrHostsUnavailable,
		},
		{
			name: "a_mix_of_unavailable_and_throttled_refusals",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{
				failedAttempt(TerminalUnavailable), failedAttempt(TerminalThrottled),
			}},
			want: ErrHostsUnavailable,
		},
		{
			name: "one_attempt_failed_a_way_that_is_not_unavailability",
			outcome: RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{
				failedAttempt(TerminalUnavailable), failedAttempt(TerminalDialFailure),
			}},
			want: ErrAllAttemptsFailed,
		},
		{
			name:    "a_trusted_host_error_still_wins_over_an_all_unavailable_race",
			outcome: failedRace(throttledWithReason),
			want:    &HostApplicationError{Type: "RateLimitError", Message: "rate limited"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := testCase.outcome.failure()

			var hostErr *HostApplicationError
			if errors.As(testCase.want, &hostErr) {
				var found *HostApplicationError
				if !errors.As(got, &found) || found.Message != hostErr.Message || found.Type != hostErr.Type {
					t.Fatalf("failure() = %v, want host error %v", got, hostErr)
				}
				return
			}
			if !errors.Is(got, testCase.want) {
				t.Fatalf("failure() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func phaseAbortedAttempt(attempt AttemptOutcome) AttemptOutcome {
	attempt.PhaseTransitionAborted = true
	return attempt
}

func failedRace(attempt AttemptOutcome) RaceOutcome {
	outcome := race(attempt)
	outcome.Succeeded = false
	return outcome
}

// Test flow:
//  1. For each table case's `EscrowMissing` flag, settle a race through a `SessionTimeouts` whose handler fails with a collection error.
//  2. Assert the final event is failed with the collection-error reason when the escrow is still there, and the escrow-gone reason when it is not.
func TestSettleTimeoutsSeparatesAGoneEscrowFromACollectionFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		escrowMissing bool
		wantReason    string
	}{
		{name: "the hosts still have the escrow", wantReason: TimeoutReasonCollectionError},
		{name: "the hosts have dropped the escrow", escrowMissing: true, wantReason: TimeoutReasonEscrowGone},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handler := &scriptedTimeoutHandler{
				result: user.TimeoutResult{Reason: "refused"},
				err:    fmt.Errorf("collect timeout votes: %w", errVoteCause),
			}
			outcome := race(failedAttempt(TerminalDialFailure))
			outcome.Lifecycle.EscrowMissing = testCase.escrowMissing

			events := settleEvents(outcome, &SessionTimeouts{handler: handler})

			settled := events[len(events)-1]
			if settled.Action != TimeoutActionFailed {
				t.Fatalf("action = %q, want %q", settled.Action, TimeoutActionFailed)
			}
			if settled.Reason != testCase.wantReason {
				t.Fatalf("reason = %q, want %q", settled.Reason, testCase.wantReason)
			}
		})
	}
}

// Test flow:
//  1. Settle a race whose escrow is marked missing through a `SessionTimeouts` wrapping a handler scripted with an applied execution result.
//  2. Assert the final event's action is completed and its reason is not the escrow-gone reason.
func TestAGoneEscrowDoesNotRenameASettledVote(t *testing.T) {
	t.Parallel()
	handler := &scriptedTimeoutHandler{
		result: user.TimeoutResult{Reason: "execution", Applied: true},
		err:    fmt.Errorf("inference %d timed out: %s", 7, "execution"),
	}
	outcome := race(failedAttempt(TerminalDialFailure))
	outcome.Lifecycle.EscrowMissing = true

	events := settleEvents(outcome, &SessionTimeouts{handler: handler})

	settled := events[len(events)-1]
	if settled.Action != TimeoutActionCompleted {
		t.Fatalf("action = %q, want %q", settled.Action, TimeoutActionCompleted)
	}
	if settled.Reason == TimeoutReasonEscrowGone {
		t.Fatal("a settled vote was named as a gone escrow")
	}
}

func engineRecordingInto(t *testing.T, windows hostWindows, hosts hostTracker) *Engine {
	t.Helper()
	deps := completeEngineDeps(t)
	deps.Windows, deps.Perf = windows, hosts
	races, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("NewEngine() = %v, want a running engine", err)
	}
	t.Cleanup(races.Stop)
	return races
}

func trackedHosts() *simTracker {
	return &simTracker{stubPerf: &stubPerf{ejected: map[string]bool{}, degraded: map[string]bool{}}}
}

// Test flow:
//  1. Build a participant limiter and an engine recording into it, then acquire half of each window for the attempt.
//  2. Record a clean outcome carrying its own input/output token counts.
//  3. Release the acquired half and read both windows back.
//  4. Assert each window widened by exactly one request's worth of room, not by the raw prompt or answer size.
func TestAnAnsweredRequestWidensEachWindowByOneRequest(t *testing.T) {
	windows := limits.NewParticipantLimiter(limiterConfig(1000), func() time.Time { return testEpoch })
	races := engineRecordingInto(t, windows, trackedHosts())
	release, admitted := windows.Acquire(testParticipant, testModel, limits.TokenCost{
		Input:  probeStartingWindow / 2,
		Output: probeStartingWindow / 2,
	})
	if admitted != limits.AdmissionOpen {
		t.Fatal("the limiter refused the attempt that fills half the window, which is the peak a healthy answer grows from")
	}
	outcome := race(cleanAttempt())
	outcome.InputTokens, outcome.OutputTokens = 1_000, 40

	races.record(outcome, nil, races.admit())
	release()

	input, output := trackedWindows(t, windows)
	if input != probeStartingWindow+probeRequestTokens {
		t.Errorf("input window = %v, want %v: an answer is worth one context of room, not the prompt it read",
			input, probeStartingWindow+probeRequestTokens)
	}
	if output != probeStartingWindow+probeRequestTokens {
		t.Errorf("output window = %v, want %v: an answer is worth one output budget of room, not the answer it produced",
			output, probeStartingWindow+probeRequestTokens)
	}
}

// Test flow:
//  1. Build a participant limiter and an engine over a host tracker reporting first-content pressure.
//  2. Record a clean outcome for that host.
//  3. Assert the input window narrows to the congestion factor its own delay signal blames, while the output window takes the smaller cross factor.
func TestRecordNarrowsTheWindowTheDelaySignalBlames(t *testing.T) {
	windows := limits.NewParticipantLimiter(limiterConfig(1000), func() time.Time { return testEpoch })
	hosts := trackedHosts()
	hosts.pressure = perf.Pressure{FirstContent: 2}
	races := engineRecordingInto(t, windows, hosts)

	races.record(race(cleanAttempt()), nil, races.admit())

	input, output := trackedWindows(t, windows)
	if math.Abs(input-probeStartingWindow*0.85) > windowTolerance {
		t.Errorf("input window = %v, want %v: a host slow to its first token is congested in prefill", input, probeStartingWindow*0.85)
	}
	if math.Abs(output-probeStartingWindow*0.90) > windowTolerance {
		t.Errorf("output window = %v, want %v: the window the delay does not blame takes the cross factor", output, probeStartingWindow*0.90)
	}
}

// Test flow:
//  1. Run a simulated race whose one host answers successfully.
//  2. Assert exactly one routing pick was made.
//  3. Assert the pick's profile carries both the request's input token count and its output token count.
func TestThePickCarriesBothHalvesOfWhatTheRequestIsWorth(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})

	if _, err := sim.run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	request := sim.profile()
	sim.picker.mu.Lock()
	profiles := append([]scheduler.RequestProfile(nil), sim.picker.profiles...)
	sim.picker.mu.Unlock()
	if len(profiles) != 1 {
		t.Fatalf("Pick calls = %d, want 1", len(profiles))
	}
	if profiles[0].InputTokens != int(request.InputTokens) {
		t.Errorf("profile input tokens = %d, want %d: a host is charged for the prompt it must prefill",
			profiles[0].InputTokens, request.InputTokens)
	}
	if profiles[0].OutputTokens != int(request.OutputTokens) {
		t.Errorf("profile output tokens = %d, want %d: a host is charged for the answer it may produce",
			profiles[0].OutputTokens, request.OutputTokens)
	}
}

// Test flow:
//  1. Build a participant limiter and an engine recording into it, and open the host's window.
//  2. Record the trusted run of escrow-lookup refusals from one host, then one more.
//  3. Assert the trusted refusals leave the input window where it was, and the one past them narrows it like a throttle.
func TestRecordStopsExcusingAHostThatOnlyRefusesLookups(t *testing.T) {
	windows := limits.NewParticipantLimiter(limiterConfig(1000), func() time.Time { return testEpoch })
	races := engineRecordingInto(t, windows, trackedHosts())
	release, admitted := windows.Acquire(testParticipant, testModel, limits.TokenCost{Input: 1, Output: 1})
	if admitted != limits.AdmissionOpen {
		t.Fatal("Acquire = refused, want the first slot of a fresh window")
	}
	release()
	start, _ := trackedWindows(t, windows)

	for range lookupRefusalsTrusted {
		races.record(race(failedAttempt(TerminalEscrowLookupLimited)), nil, races.admit())
	}
	trusted, _ := trackedWindows(t, windows)
	races.record(race(failedAttempt(TerminalEscrowLookupLimited)), nil, races.admit())
	judged, _ := trackedWindows(t, windows)

	if trusted != start {
		t.Fatalf("input window after %d lookup refusals = %v, want %v unchanged", lookupRefusalsTrusted, trusted, start)
	}
	if judged >= trusted {
		t.Fatalf("input window after one refusal past the trusted run = %v, want below %v: it is judged as a throttle", judged, trusted)
	}
}
