package engine

import (
	"context"
	"math"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/transport"
	"devshard/types"
)

const (
	testParticipant = "gonka1host"
	testModel       = "Qwen/Qwen3-235B"
)

var testEpoch = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

const (
	probeRequestTokens    = 16
	probeStartingRequests = 16
	probeStartingWindow   = probeStartingRequests * probeRequestTokens
)

var probeAttempt = limits.TokenCost{Input: probeStartingWindow, Output: probeStartingWindow}

func limiterConfig(tripThreshold int64) limits.ParticipantConfig {
	return limits.ParticipantConfig{
		Pricing: limits.WindowPricing{
			Input:                 limits.RequestBounds{Min: 1, Initial: probeStartingRequests},
			Output:                limits.RequestBounds{Min: 1, Initial: probeStartingRequests},
			FallbackContextTokens: probeRequestTokens,
			FallbackOutputTokens:  probeRequestTokens,
		},
		Factors:       limits.CongestionFactors{Soft: 0.85, Hard: 0.70, Severe: 0.50, Cross: 0.90},
		AfterFailures: tripThreshold,
		BaseOpen:      time.Minute,
		MaxOpen:       time.Hour,
	}
}

// observedWindows drives one verdict through a real limiter and reads both windows back off its snapshot.
func observedWindows(t *testing.T, verdict limits.Verdict, recorded bool) (input, output float64) {
	t.Helper()

	limiter := limits.NewParticipantLimiter(limiterConfig(1000), func() time.Time { return testEpoch })
	release, _ := limiter.Acquire(testParticipant, testModel, probeAttempt)
	if recorded {
		limiter.OnResult(probeResult(verdict))
	}
	release()

	return trackedWindows(t, limiter)
}

func trackedWindows(t *testing.T, limiter *limits.ParticipantLimiter) (input, output float64) {
	t.Helper()

	tracked := limiter.Snapshot()
	if len(tracked) != 1 {
		t.Fatalf("limiter tracks %d pairs, want the one host the test drove it with", len(tracked))
	}
	return tracked[0].InputWindowTokens, tracked[0].OutputWindowTokens
}

func observedCutoffOpen(verdict limits.Verdict, recorded bool) bool {
	limiter := limits.NewParticipantLimiter(limiterConfig(1), func() time.Time { return testEpoch })
	if recorded {
		limiter.OnResult(probeResult(verdict))
	}
	return !limiter.Available(testParticipant, testModel)
}

func probeResult(verdict limits.Verdict) limits.Result {
	return limits.Result{
		Participant: testParticipant,
		Model:       testModel,
		Verdict:     verdict,
		Carried:     probeAttempt,
	}
}

const windowTolerance = 0.5

func assertWindowsAndCutoff(t *testing.T, verdict limits.Verdict, recorded bool, wantInput, wantOutput float64, wantCutoff bool) {
	t.Helper()

	input, output := observedWindows(t, verdict, recorded)
	if math.Abs(input-wantInput) > windowTolerance {
		t.Errorf("input window = %v, want %v: the tier this verdict blames prefill with, or the cross factor when it blames decode", input, wantInput)
	}
	if math.Abs(output-wantOutput) > windowTolerance {
		t.Errorf("output window = %v, want %v: the tier this verdict blames decode with, or the cross factor when it blames prefill", output, wantOutput)
	}
	if open := observedCutoffOpen(verdict, recorded); open != wantCutoff {
		t.Errorf("cutoff open = %v, want %v: only a fault the host never answered for counts towards cutting it off", open, wantCutoff)
	}
}

func cleanAttempt() AttemptOutcome {
	return AttemptOutcome{
		Participant:   testParticipant,
		Nonce:         7,
		Role:          "primary",
		StartedAt:     testEpoch,
		SendTime:      testEpoch,
		ReceiptTime:   testEpoch.Add(200 * time.Millisecond),
		FirstToken:    testEpoch.Add(900 * time.Millisecond),
		Completed:     testEpoch.Add(3 * time.Second),
		ContentChunks: 12,
		Terminal:      TerminalWon,
		Confirmed:     true,
		NonceFinished: true,
	}
}

func failedAttempt(terminal Terminal) AttemptOutcome {
	attempt := cleanAttempt()
	attempt.Terminal = terminal
	attempt.Confirmed = false
	attempt.NonceFinished = false
	attempt.ContentChunks = 0
	return attempt
}

func race(attempt AttemptOutcome) RaceOutcome {
	return RaceOutcome{
		RequestID:            "req-1",
		EscrowID:             "escrow-1",
		Model:                testModel,
		InputTokens:          1024,
		InputTokensFromBytes: 1024,
		WinnerNonce:          7,
		Succeeded:            true,
		Attempts:             []AttemptOutcome{attempt},
	}
}

// Test flow:
//  1. Build a table covering every upstream condition the verdict specification classifies: a clean finish by the winner and by a loser, missed first-token and receipt deadlines, overload (429/503), transport faults (404/403/401-drift/dial-failure/unexpected-EOF/truncated-stream), on-path and off-path rejections, an empty stream that never finished its nonce versus one that did (a finished nonce still counts as an answer; an unfinished one keeps its reserve parked for the timeout vote), a burned-token empty stream, an error-stream and a capability refusal, a stalled winner, a long response past the exemption (marked by ContentSource so it reads as a long response rather than a silent error-only attempt), the PoC bypass, a phase-transition abort, an escrow-missing signal, state-root divergence, and a still-unfinished clean attempt.
//  2. For each case, assert Verdict() returns the table's verdict and recorded flag.
//  3. Drive that verdict through a real limiter and assert the resulting input/output windows and cutoff state match the table.
func TestVerdictTable(t *testing.T) {
	stalled := failedAttempt(TerminalStalled)
	stalled.ContentChunks = 5

	longResponse := cleanAttempt()
	longResponse.NonceFinished = false
	longResponse.ContentSource = "delta.content"
	longResponse.Completed = testEpoch.Add(longResponseExemption)

	unfinished := cleanAttempt()
	unfinished.NonceFinished = false

	heldEmpty := failedAttempt(TerminalEmptyStream)
	heldEmpty.Completed = testEpoch.Add(emptyStreamHeldTooLong)

	finishedEmpty := failedAttempt(TerminalEmptyStream)
	finishedEmpty.NonceFinished = true

	heldFinishedEmpty := finishedEmpty
	heldFinishedEmpty.Completed = testEpoch.Add(emptyStreamHeldTooLong)

	heldBurnEmpty := failedAttempt(TerminalBurnEmpty)
	heldBurnEmpty.Completed = testEpoch.Add(15 * time.Minute)

	briefBurnEmpty := failedAttempt(TerminalBurnEmpty)
	briefBurnEmpty.Completed = testEpoch.Add(emptyStreamHeldTooLong - time.Millisecond)

	lateWinner := cleanAttempt()
	lateWinner.FirstTokenDeadlineMissed = true

	lateLoser := cleanAttempt()
	lateLoser.Terminal = TerminalLost
	lateLoser.Nonce = 8
	lateLoser.ReceiptDeadlineMissed = true

	lateThrottled := failedAttempt(TerminalThrottled)
	lateThrottled.ReceiptDeadlineMissed = true

	tests := []struct {
		name             string
		outcome          RaceOutcome
		attempt          AttemptOutcome
		wantVerdict      limits.Verdict
		wantRecorded     bool
		wantInputWindow  float64
		wantOutputWindow float64
		wantCutoff       bool
	}{
		{"clean finish, nonce finished, content present", race(cleanAttempt()), cleanAttempt(), limits.Success, true, 272, 272, false},
		{"clean finish by a loser", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.Terminal = TerminalLost
			attempt.Nonce = 8
			return attempt
		}(), limits.Success, true, 272, 272, false},
		{"clean finish after missing the first-token deadline", race(lateWinner), lateWinner, limits.LateSuccess, true, 256, 256, false},
		{"clean finish by a loser after missing the receipt deadline", race(cleanAttempt()), lateLoser, limits.LateSuccess, true, 256, 256, false},
		{"http 429 after missing the receipt deadline", race(cleanAttempt()), lateThrottled, limits.Overload, true, 217.6, 217.6, false},
		{"http 429", race(cleanAttempt()), failedAttempt(TerminalThrottled), limits.Overload, true, 217.6, 217.6, false},
		{"http 503", race(cleanAttempt()), failedAttempt(TerminalUnavailable), limits.Overload, true, 217.6, 217.6, false},
		{"http 404 on the inference path", race(cleanAttempt()), failedAttempt(TerminalNotFound), limits.TransportFault, true, 256, 256, true},
		{"http 403 on the inference path", race(cleanAttempt()), failedAttempt(TerminalForbidden), limits.TransportFault, true, 256, 256, true},
		{"http 401 with timestamp drift", race(cleanAttempt()), failedAttempt(TerminalTimestampDrift), limits.TransportFault, true, 256, 256, true},
		{"http 400 / 500 / any other status", race(cleanAttempt()), failedAttempt(TerminalRejected), limits.ModelOutcome, false, 256, 256, false},
		{"dial error, connection reset, TLS failure", race(cleanAttempt()), failedAttempt(TerminalDialFailure), limits.TransportFault, true, 256, 256, true},
		{"unexpected EOF", race(cleanAttempt()), failedAttempt(TerminalUnexpectedEOF), limits.TransportFault, true, 256, 256, true},
		{"truncated SSE stream", race(cleanAttempt()), failedAttempt(TerminalStreamTruncated), limits.TransportFault, true, 256, 256, true},
		{"failure on a non-inference path", race(cleanAttempt()), failedAttempt(TerminalOffPath), limits.ModelOutcome, false, 256, 256, false},
		{"empty stream that never finished its nonce", race(failedAttempt(TerminalEmptyStream)), failedAttempt(TerminalEmptyStream), limits.EmptyAnswerLeftOpen, true, 128, 128, true},
		{"empty stream that finished its nonce", race(finishedEmpty), finishedEmpty, limits.EmptyAnswer, true, 179.2, 179.2, false},
		{"empty stream with completion tokens burned", race(cleanAttempt()), failedAttempt(TerminalBurnEmpty), limits.ModelOutcome, true, 256, 256, false},
		{"error event inside the SSE stream", race(cleanAttempt()), failedAttempt(TerminalErrorStream), limits.ModelOutcome, true, 256, 256, false},
		{"capability refusal another host can serve", race(cleanAttempt()), failedAttempt(TerminalCapabilityRefused), limits.ModelOutcome, true, 256, 256, false},
		{"winner stalled after content", race(stalled), stalled, limits.DecodeStalled, true, 230.4, 128, false},
		{"content produced, past the exemption, nonce unfinished", race(longResponse), longResponse, limits.ModelOutcome, false, 256, 256, false},
		{"empty stream that held the request past the refusal point without finishing its nonce", race(heldEmpty), heldEmpty, limits.EmptyAnswerLeftOpen, true, 128, 128, true},
		{"empty stream that finished its nonce and held the request past the refusal point", race(heldFinishedEmpty), heldFinishedEmpty, limits.EmptyAnswer, true, 179.2, 179.2, false},
		{"empty stream that burned tokens and held the request", race(heldBurnEmpty), heldBurnEmpty, limits.Overload, true, 217.6, 217.6, false},
		{"empty stream that burned tokens one millisecond inside the refusal point", race(briefBurnEmpty), briefBurnEmpty, limits.ModelOutcome, true, 256, 256, false},
		{"empty stream that held the request while the PoC bypass is active", func() RaceOutcome {
			outcome := race(heldEmpty)
			outcome.PoCBypassActive = true
			return outcome
		}(), heldEmpty, limits.ModelOutcome, false, 256, 256, false},
		{"empty stream while the PoC bypass is active", func() RaceOutcome {
			outcome := race(failedAttempt(TerminalEmptyStream))
			outcome.PoCBypassActive = true
			return outcome
		}(), failedAttempt(TerminalEmptyStream), limits.ModelOutcome, false, 256, 256, false},
		{"attempt aborted by a phase transition", race(cleanAttempt()), func() AttemptOutcome {
			attempt := failedAttempt(TerminalEmptyStream)
			attempt.PhaseTransitionAborted = true
			return attempt
		}(), limits.ModelOutcome, false, 256, 256, false},
		{"escrow not found", func() RaceOutcome {
			outcome := race(failedAttempt(TerminalRejected))
			outcome.Lifecycle.EscrowMissing = true
			return outcome
		}(), failedAttempt(TerminalRejected), limits.ModelOutcome, false, 256, 256, false},
		{"post-state-root divergence", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.StateDivergent = true
			return attempt
		}(), limits.ModelOutcome, false, 256, 256, false},
		{"clean finish with the nonce still unfinished", race(unfinished), unfinished, limits.ModelOutcome, false, 256, 256, false},
		{"client cancelled", race(cleanAttempt()), failedAttempt(TerminalClientCancelled), limits.ModelOutcome, false, 256, 256, false},
		{"send returned without a receipt", race(cleanAttempt()), failedAttempt(TerminalNoReceipt), limits.ModelOutcome, false, 256, 256, false},
		{"unclassified", race(cleanAttempt()), failedAttempt(TerminalUnclassified), limits.ModelOutcome, false, 256, 256, false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			verdict, recorded := testCase.outcome.Verdict(testCase.attempt)
			if verdict != testCase.wantVerdict || recorded != testCase.wantRecorded {
				t.Fatalf("Verdict() = (%v, %v), want (%v, %v)", verdict, recorded, testCase.wantVerdict, testCase.wantRecorded)
			}
			assertWindowsAndCutoff(t, verdict, recorded, testCase.wantInputWindow, testCase.wantOutputWindow, testCase.wantCutoff)
		})
	}
}

// Test flow:
//  1. Build a table of the two deadlines a still-running attempt can miss (receipt, first-token) with the verdict each should produce.
//  2. For each stage, assert missedDeadlineVerdict returns the table's verdict.
//  3. Drive that verdict through a real limiter and assert the resulting windows and cutoff match the table.
func TestMissedDeadlineVerdictTable(t *testing.T) {
	tests := []struct {
		name             string
		stage            EscalationStage
		wantVerdict      limits.Verdict
		wantInputWindow  float64
		wantOutputWindow float64
		wantCutoff       bool
	}{
		{"receipt deadline missed", StageReceiptTimeout, limits.MissedReceiptDeadline, 128, 128, false},
		{"first-token deadline missed", StageFirstToken, limits.MissedFirstTokenDeadline, 128, 230.4, false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			verdict := missedDeadlineVerdict(testCase.stage)
			if verdict != testCase.wantVerdict {
				t.Fatalf("missedDeadlineVerdict() = %v, want %v", verdict, testCase.wantVerdict)
			}
			assertWindowsAndCutoff(t, verdict, true, testCase.wantInputWindow, testCase.wantOutputWindow, testCase.wantCutoff)
		})
	}
}

// Test flow:
//  1. Build a table of every exemption rung: a healthy attempt, a phase-transition abort, an error stream, a capability refusal, state-root divergence, a long response past the exemption (marked by ContentSource, distinct from a silent error-only attempt), an empty stream under the PoC bypass, an empty stream in a race nobody won, an empty stream in a race someone else won, a client-cancelled loser, and an assignment no attempt could spend.
//  2. For each case, assert sampleExemption returns the table's exemption.
//  3. Assert Sample reports a sample only for the cases the table expects SampleRecorded.
func TestSampleExemptionLadderRungs(t *testing.T) {
	longResponse := cleanAttempt()
	longResponse.NonceFinished = false
	longResponse.ContentSource = "delta.content"
	longResponse.Completed = testEpoch.Add(longResponseExemption)

	tests := []struct {
		name    string
		outcome RaceOutcome
		attempt AttemptOutcome
		want    SampleExemption
	}{
		{"healthy attempt", race(cleanAttempt()), cleanAttempt(), SampleRecorded},
		{"phase transition abort", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.PhaseTransitionAborted = true
			return attempt
		}(), ExemptPhaseAborted},
		{"error stream", race(cleanAttempt()), failedAttempt(TerminalErrorStream), ExemptErrorStream},
		{"capability refusal", race(cleanAttempt()), failedAttempt(TerminalCapabilityRefused), ExemptErrorStream},
		{"state-root divergence", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.StateDivergent = true
			return attempt
		}(), ExemptStateDivergent},
		{"long response", race(longResponse), longResponse, ExemptLongResponse},
		{"empty stream under the PoC bypass", func() RaceOutcome {
			outcome := race(failedAttempt(TerminalEmptyStream))
			outcome.PoCBypassActive = true
			return outcome
		}(), failedAttempt(TerminalEmptyStream), ExemptPoCSuppressed},
		{"empty stream in a race nobody won", func() RaceOutcome {
			outcome := race(failedAttempt(TerminalEmptyStream))
			outcome.Succeeded = false
			return outcome
		}(), failedAttempt(TerminalEmptyStream), ExemptEmptyStreamNoWinner},
		{"empty stream in a race someone else won", race(failedAttempt(TerminalEmptyStream)), failedAttempt(TerminalEmptyStream), SampleRecorded},
		{"a loser the race itself cancelled", race(cleanAttempt()), failedAttempt(TerminalClientCancelled), ExemptClientCancelled},
		{"an assignment no attempt could spend", race(cleanAttempt()), AttemptOutcome{Participant: testParticipant, Terminal: TerminalNoReceipt}, ExemptNeverDispatched},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.outcome.sampleExemption(testCase.attempt); got != testCase.want {
				t.Fatalf("sampleExemption() = %v, want %v", got, testCase.want)
			}
			_, exemption := testCase.outcome.Sample(testCase.attempt)
			if (exemption == SampleRecorded) != (testCase.want == SampleRecorded) {
				t.Fatalf("Sample() exemption = %v, want a sample: %v", exemption, testCase.want == SampleRecorded)
			}
		})
	}
}

// Test flow:
//  1. Build one attempt that is both phase-transition-aborted and state-divergent, and another that is both state-divergent and racing under an active PoC bypass.
//  2. Assert sampleExemption reports the earlier rung in each case: phase abort before state divergence, and state divergence before the PoC bypass.
func TestSampleExemptionLadderOrder(t *testing.T) {
	abortedAndDivergent := failedAttempt(TerminalEmptyStream)
	abortedAndDivergent.PhaseTransitionAborted = true
	abortedAndDivergent.StateDivergent = true

	divergentAndPoCSuppressed := failedAttempt(TerminalEmptyStream)
	divergentAndPoCSuppressed.StateDivergent = true

	pocSuppressedRace := race(divergentAndPoCSuppressed)
	pocSuppressedRace.PoCBypassActive = true

	tests := []struct {
		name    string
		outcome RaceOutcome
		attempt AttemptOutcome
		want    SampleExemption
	}{
		{"phase abort before state divergence", race(abortedAndDivergent), abortedAndDivergent, ExemptPhaseAborted},
		{"state divergence before the PoC bypass", pocSuppressedRace, divergentAndPoCSuppressed, ExemptStateDivergent},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.outcome.sampleExemption(testCase.attempt); got != testCase.want {
				t.Fatalf("sampleExemption() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a race around a clean, winning attempt.
//  2. Call Sample and assert it returns SampleRecorded.
//  3. Assert the sample's participant, model, and responsiveness fields match the attempt.
func TestSampleFields(t *testing.T) {
	t.Parallel()

	outcome := race(cleanAttempt())
	sample, exemption := outcome.Sample(cleanAttempt())
	if exemption != SampleRecorded {
		t.Fatalf("Sample() exemption = %v, want SampleRecorded", exemption)
	}

	want := perf.Sample{
		ParticipantKey: testParticipant,
		Model:          testModel,
		Responsive:     true,
	}
	if sample != want {
		t.Fatalf("Sample() = %+v, want %+v", sample, want)
	}
}

// Test flow:
//  1. Build a table of attempt shapes: a clean finish, a nonce left unfinished, a receipt left unconfirmed, and an empty stream that finished its nonce and was confirmed.
//  2. For each case, call Sample and assert it returns SampleRecorded.
//  3. Assert the sample's Responsive field matches the table's expectation.
func TestSampleResponsive(t *testing.T) {
	tests := []struct {
		name    string
		outcome RaceOutcome
		attempt AttemptOutcome
		want    bool
	}{
		{"clean finish", race(cleanAttempt()), cleanAttempt(), true},
		{"nonce unfinished", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.NonceFinished = false
			return attempt
		}(), false},
		{"receipt unconfirmed", race(cleanAttempt()), func() AttemptOutcome {
			attempt := cleanAttempt()
			attempt.Confirmed = false
			return attempt
		}(), false},
		{"empty stream", race(failedAttempt(TerminalEmptyStream)), func() AttemptOutcome {
			attempt := failedAttempt(TerminalEmptyStream)
			attempt.Confirmed = true
			attempt.NonceFinished = true
			return attempt
		}(), false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sample, exemption := testCase.outcome.Sample(testCase.attempt)
			if exemption != SampleRecorded {
				t.Fatalf("Sample() exemption = %v, want SampleRecorded", exemption)
			}
			if sample.Responsive != testCase.want {
				t.Fatalf("Sample().Responsive = %v, want %v", sample.Responsive, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a race around an empty-stream attempt whose nonce never finished.
//  2. Assert Verdict reports EmptyAnswerLeftOpen and that it is recorded.
//  3. Drive that verdict through a real limiter and assert both windows narrow to 128 and the cutoff opens.
//  4. Assert DeniesCrowning reports true.
func TestEmptyStreamWithAnUnfinishedNonceDeniesCrowningAndOpensTheCutoff(t *testing.T) {
	t.Parallel()

	attempt := failedAttempt(TerminalEmptyStream)
	outcome := race(attempt)

	verdict, recorded := outcome.Verdict(attempt)
	if verdict != limits.EmptyAnswerLeftOpen || !recorded {
		t.Fatalf("Verdict() = (%v, %v), want (EmptyAnswerLeftOpen, true)", verdict, recorded)
	}
	input, output := observedWindows(t, verdict, recorded)
	if math.Abs(input-128) > windowTolerance || math.Abs(output-128) > windowTolerance {
		t.Fatalf("windows after an empty stream = %v/%v, want 128/128: a nonce left open narrows both severely", input, output)
	}
	if !observedCutoffOpen(verdict, recorded) {
		t.Fatal("cutoff after an empty stream = closed, want open")
	}
	if !outcome.DeniesCrowning(attempt) {
		t.Fatal("DeniesCrowning() = false, want true")
	}
}

// Test flow:
//  1. Build a table of attempt shapes: an empty stream, a burned-token empty stream, a clean finish, a phase-transition-aborted empty stream, and an empty stream under an active PoC bypass.
//  2. For each case, assert DeniesCrowning matches the table's expectation.
func TestDeniesCrowning(t *testing.T) {
	tests := []struct {
		name    string
		outcome RaceOutcome
		attempt AttemptOutcome
		want    bool
	}{
		{"empty stream", race(failedAttempt(TerminalEmptyStream)), failedAttempt(TerminalEmptyStream), true},
		{"burned completion tokens", race(failedAttempt(TerminalBurnEmpty)), failedAttempt(TerminalBurnEmpty), false},
		{"clean finish", race(cleanAttempt()), cleanAttempt(), false},
		{"phase transition abort", race(cleanAttempt()), func() AttemptOutcome {
			attempt := failedAttempt(TerminalEmptyStream)
			attempt.PhaseTransitionAborted = true
			return attempt
		}(), false},
		{"PoC bypass active", func() RaceOutcome {
			outcome := race(failedAttempt(TerminalEmptyStream))
			outcome.PoCBypassActive = true
			return outcome
		}(), failedAttempt(TerminalEmptyStream), false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.outcome.DeniesCrowning(testCase.attempt); got != testCase.want {
				t.Fatalf("DeniesCrowning() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build attempt variants: a winner, a suppressed loser, a shadow-quarantined host whose loser attempt another host served for, a shadow-quarantined host that served alone, a role-less attempt, an unfinished nonce, a throttled attempt, a truncated stream, and a phase-transition-aborted empty stream.
//  2. For each case, assert Labels returns the table's participant, model, role, outcome, visibility, and reason.
func TestLabels(t *testing.T) {
	loser := cleanAttempt()
	loser.Terminal = TerminalLost
	loser.Nonce = 8

	suspicious := cleanAttempt()
	suspicious.Suspicious = true
	suspicious.Terminal = TerminalLost
	suspicious.Nonce = 8

	suspiciousWinner := cleanAttempt()
	suspiciousWinner.Suspicious = true

	unfinished := cleanAttempt()
	unfinished.NonceFinished = false

	aborted := failedAttempt(TerminalEmptyStream)
	aborted.PhaseTransitionAborted = true

	roleless := cleanAttempt()
	roleless.Role = ""

	tests := []struct {
		name       string
		outcome    RaceOutcome
		attempt    AttemptOutcome
		wantLabels AttemptLabels
	}{
		{"winner", race(cleanAttempt()), cleanAttempt(), AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "success", Visibility: "user_visible_winner",
		}},
		{"suppressed loser", race(cleanAttempt()), loser, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "success", Visibility: "suppressed_loser",
		}},
		{"shadow-quarantined host another attempt served for", race(cleanAttempt()), suspicious, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "success", Visibility: "no_winner",
		}},
		{"shadow-quarantined host that served alone", race(suspiciousWinner), suspiciousWinner, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "success", Visibility: "user_visible_winner",
		}},
		{"missing role defaults to primary", race(roleless), roleless, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "success", Visibility: "user_visible_winner",
		}},
		{"nonce unfinished", race(unfinished), unfinished, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "failed", Visibility: "failed_not_finished", Reason: "not_finished",
		}},
		{"http 429", race(cleanAttempt()), failedAttempt(TerminalThrottled), AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "failed", Visibility: "failed_not_finished", Reason: "http_429",
		}},
		{"truncated stream", race(cleanAttempt()), failedAttempt(TerminalStreamTruncated), AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "failed", Visibility: "failed_not_finished", Reason: "sse_truncated",
		}},
		{"phase abort outranks the terminal reason", race(aborted), aborted, AttemptLabels{
			Participant: testParticipant, Model: testModel, Role: "primary",
			Outcome: "failed", Visibility: "failed_not_finished", Reason: "phase_transition_aborted",
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			labels := testCase.outcome.Labels(testCase.attempt)
			if labels != testCase.wantLabels {
				t.Fatalf("Labels() = %+v, want %+v", labels, testCase.wantLabels)
			}
		})
	}
}

// Test flow:
//  1. Build a table pairing every terminal with the failure reason string it should report.
//  2. For each terminal, assert reason() returns the table's string.
func TestTerminalFailureReasons(t *testing.T) {
	tests := []struct {
		terminal Terminal
		want     string
	}{
		{TerminalUnclassified, "unknown"},
		{TerminalWon, ""},
		{TerminalLost, ""},
		{TerminalThrottled, "http_429"},
		{TerminalUnavailable, "http_503"},
		{TerminalForbidden, "http_forbidden"},
		{TerminalNotFound, "http_not_found"},
		{TerminalTimestampDrift, "http_timestamp_drift"},
		{TerminalRejected, "http_error"},
		{TerminalOffPath, "off_path"},
		{TerminalDialFailure, "transport_error"},
		{TerminalStreamTruncated, "sse_truncated"},
		{TerminalUnexpectedEOF, "eof_transport"},
		{TerminalClientCancelled, "client_cancelled"},
		{TerminalNoReceipt, "no_receipt"},
		{TerminalEmptyStream, "empty_stream"},
		{TerminalBurnEmpty, "empty_stream"},
		{TerminalErrorStream, "error_stream"},
		{TerminalCapabilityRefused, "error_stream"},
		{TerminalStalled, "stalled"},
	}

	for _, testCase := range tests {
		if got := testCase.terminal.reason(); got != testCase.want {
			t.Errorf("Terminal(%d).reason() = %q, want %q", testCase.terminal, got, testCase.want)
		}
	}
}

// Test flow:
//  1. Build two races around the same clean attempt, one with no lifecycle signals and one with EscrowMissing and BalanceExhausted both set.
//  2. Assert Verdict, Sample, Labels, and DeniesCrowning all return the same result for both races.
func TestLifecycleSignalsAreInert(t *testing.T) {
	t.Parallel()

	attempt := cleanAttempt()
	quiet := race(attempt)
	signalled := race(attempt)
	signalled.Lifecycle = Lifecycle{EscrowMissing: true, BalanceExhausted: true}

	quietVerdict, quietRecorded := quiet.Verdict(attempt)
	signalledVerdict, signalledRecorded := signalled.Verdict(attempt)
	if quietVerdict != signalledVerdict || quietRecorded != signalledRecorded {
		t.Errorf("Verdict() changed with lifecycle signals: (%v, %v) vs (%v, %v)", quietVerdict, quietRecorded, signalledVerdict, signalledRecorded)
	}

	quietSample, quietExemption := quiet.Sample(attempt)
	signalledSample, signalledExemption := signalled.Sample(attempt)
	if quietSample != signalledSample || quietExemption != signalledExemption {
		t.Errorf("Sample() changed with lifecycle signals: %+v/%v vs %+v/%v", quietSample, quietExemption, signalledSample, signalledExemption)
	}

	quietLabels := quiet.Labels(attempt)
	signalledLabels := signalled.Labels(attempt)
	if quietLabels != signalledLabels {
		t.Errorf("Labels() changed with lifecycle signals: %+v vs %+v", quietLabels, signalledLabels)
	}
	if quiet.DeniesCrowning(attempt) != signalled.DeniesCrowning(attempt) {
		t.Error("DeniesCrowning() changed with lifecycle signals")
	}
}

// Test flow:
//  1. For every terminal/status pair in terminalStatuses, build an UpstreamStatusError carrying that status (and the timestamp-drift body when the terminal calls for it).
//  2. Classify the error and assert it maps back to the same terminal.
//  3. Assert StatusFor recovers the same status code from that terminal.
func TestEveryRecoveredStatusRoundTripsThroughItsTerminal(t *testing.T) {
	for terminal, status := range terminalStatuses {
		body := ""
		if terminal == TerminalTimestampDrift {
			body = "timestamp drift detected"
		}
		err := &transport.UpstreamStatusError{Path: "/v1/chat/completions", StatusCode: status, Body: body}

		classified := classifyDispatchError(context.Background(), err)

		if classified != terminal {
			t.Errorf("status %d classified as %v, want %v", status, classified, terminal)
		}
		if recovered, ok := StatusFor(classified); !ok || recovered != status {
			t.Errorf("StatusFor(%v) = %d/%v, want %d/true", classified, recovered, ok, status)
		}
	}
}

// Test flow:
//  1. Walk every terminal from TerminalUnclassified to TerminalHardTimeout.
//  2. Assert each one has a non-empty name that is not the unnamed/unknown placeholder.
//  3. Assert a win and a loss both report an empty failure reason.
func TestEveryTerminalHasAName(t *testing.T) {
	for terminal := TerminalUnclassified; terminal <= TerminalHardTimeout; terminal++ {
		if terminal.String() == "" {
			t.Fatalf("terminal %d has no name", terminal)
		}
		if terminal.String() == TerminalNameUnnamed || terminal.String() == ReasonUnknown {
			t.Fatalf("terminal %d fell through to a placeholder, so a new terminal was added without a name", terminal)
		}
	}
	if TerminalWon.reason() != "" || TerminalLost.reason() != "" {
		t.Fatal("a win or a loss reported a failure reason, which failureReason would report as the cause")
	}
}

// Test flow:
//  1. Build an outcome with one attempt that was dispatched (SendTime set) and never classified past TerminalUnclassified.
//  2. Call TimeoutPlan and assert it returns one step for that nonce with Post set, since the nonce was spent on chain and never finished.
//  3. Assert Sample exempts it as ExemptNeverReported.
func TestAnAttemptThatNeverReportedStillOwesAVote(t *testing.T) {
	outcome := RaceOutcome{
		Model: testModel,
		Attempts: []AttemptOutcome{{
			Nonce:       557,
			Participant: testParticipant,
			SendTime:    raceStart,
			Terminal:    TerminalUnclassified,
		}},
	}

	plan := outcome.TimeoutPlan()

	if len(plan) != 1 || plan[0].Nonce != 557 {
		t.Fatalf("TimeoutPlan() = %+v, want one step for nonce 557", plan)
	}
	if !plan[0].Post {
		t.Fatalf("step = %+v, want the vote posted: the nonce was spent and never finished", plan[0])
	}
	if _, exemption := outcome.Sample(outcome.Attempts[0]); exemption != ExemptNeverReported {
		t.Fatalf("exemption = %v, want the host judged for nothing it never answered", exemption)
	}
}

// Test flow:
//  1. Build an attempt whose first content arrived 5 seconds in and last chunk 15 seconds in, with 100 completion tokens reported.
//  2. Assert TimePerOutputToken divides only the 10-second decode window by the token count, not the prefill time before first content.
func TestTimePerOutputTokenMeasuresTheDecodeWindowAlone(t *testing.T) {
	t.Parallel()
	attempt := cleanAttempt()
	attempt.FirstContent = testEpoch.Add(5 * time.Second)
	attempt.LastChunk = testEpoch.Add(15 * time.Second)
	attempt.UsageCompletionTokens = 100

	if got, want := TimePerOutputToken(attempt), 100*time.Millisecond; got != want {
		t.Fatalf("TimePerOutputToken() = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Build a baseline attempt with a measurable decode window and completion token count.
//  2. Vary it per case: no completion tokens reported, content never arriving, a one-chunk-wide answer, or a last chunk that predates the first.
//  3. Assert TimePerOutputToken reports 0 for every case that leaves no measurable window.
func TestTimePerOutputTokenIsUnreportedWithoutAMeasurableWindow(t *testing.T) {
	t.Parallel()
	measurable := cleanAttempt()
	measurable.FirstContent = testEpoch.Add(5 * time.Second)
	measurable.LastChunk = testEpoch.Add(15 * time.Second)
	measurable.UsageCompletionTokens = 100

	cases := []struct {
		name   string
		mutate func(*AttemptOutcome)
	}{
		{"the host reported no completion tokens", func(a *AttemptOutcome) { a.UsageCompletionTokens = 0 }},
		{"content never arrived", func(a *AttemptOutcome) { a.FirstContent = time.Time{} }},
		{"the answer was one chunk wide", func(a *AttemptOutcome) { a.LastChunk = a.FirstContent }},
		{"the last chunk predates the first", func(a *AttemptOutcome) { a.LastChunk = testEpoch }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			attempt := measurable
			testCase.mutate(&attempt)

			if got := TimePerOutputToken(attempt); got != 0 {
				t.Fatalf("TimePerOutputToken() = %v, want 0", got)
			}
		})
	}
}

// Test flow:
//  1. Build an attempt with a 2-second decode window and 40 completion tokens.
//  2. Call Sample and assert it is recorded.
//  3. Assert the sample's TimePerOutputToken matches the decode measure.
func TestSampleCarriesTheDecodeMeasure(t *testing.T) {
	t.Parallel()
	attempt := cleanAttempt()
	attempt.FirstContent = testEpoch.Add(time.Second)
	attempt.LastChunk = testEpoch.Add(3 * time.Second)
	attempt.UsageCompletionTokens = 40

	sample, exemption := race(attempt).Sample(attempt)

	if exemption != SampleRecorded {
		t.Fatalf("Sample() exemption = %v, want it recorded", exemption)
	}
	if got, want := sample.TimePerOutputToken, 50*time.Millisecond; got != want {
		t.Fatalf("sample.TimePerOutputToken = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Build a table of attempt shapes: content answered, an empty stream, never reaching the host, the client leaving, and a win that was never confirmed.
//  2. For each case, assert JudgesCrowning matches the table's expectation, so a host alternating empty streams with dial failures cannot dodge the crown-denial strike threshold.
func TestOnlyAnAnswerOrAnEmptyStreamJudgesCrowning(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		attempt AttemptOutcome
		want    bool
	}{
		{name: "answered with content", attempt: cleanAttempt(), want: true},
		{name: "claimed to serve and produced none", attempt: failedAttempt(TerminalEmptyStream), want: true},
		{name: "never reached the host", attempt: failedAttempt(TerminalNoReceipt), want: false},
		{name: "the client left", attempt: failedAttempt(TerminalClientCancelled), want: false},
		{name: "won but never confirmed", attempt: failedAttempt(TerminalWon), want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := race(testCase.attempt).JudgesCrowning(testCase.attempt); got != testCase.want {
				t.Fatalf("JudgesCrowning() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a crown-strikes tracker and record all but one of the strikes needed to deny a host's crown.
//  2. Report a race the host never answered (TerminalNoReceipt) through observeCrowning.
//  3. Record the final strike and assert the host is denied, proving the never-answered race left its strikes untouched.
func TestARaceTheHostNeverAnsweredLeavesItsStrikesAlone(t *testing.T) {
	t.Parallel()
	crown := newCrownStrikes(nil)
	for range crownDenialStrikes - 1 {
		crown.Observe(testParticipant, testModel, true)
	}

	race(failedAttempt(TerminalNoReceipt)).observeCrowning(crown)
	crown.Observe(testParticipant, testModel, true)

	if !crown.Denied(testParticipant, testModel) {
		t.Fatal("the dial failure cleared the strikes the host had already earned")
	}
}

// Test flow:
//  1. Build an attempt that failed because the gateway's own oversize cap killed it.
//  2. Assert Verdict reports ModelOutcome, since the response-too-large cap says nothing about the host's transport.
func TestResponseTooLargeIsNotChargedToTheHostAsATransportFault(t *testing.T) {
	t.Parallel()

	attempt := failedAttempt(TerminalResponseTooLarge)
	outcome := race(attempt)

	verdict, recorded := outcome.Verdict(attempt)
	if verdict != limits.ModelOutcome {
		t.Fatalf("Verdict() = (%v, %v), want (ModelOutcome, _)", verdict, recorded)
	}
}

// Test flow:
//  1. Build a table of upstream status/body pairs: bare 500/502/504 server errors, a 400 client error, and 500s whose body names a fault the gateway caused (escrow not found, nonce cap exceeded, prompt hash mismatch).
//  2. Classify each error and assert it lands on the table's terminal.
//  3. Assert Verdict reports the table's verdict, moving the window only for the bare 5xx server errors and never for the 4xx or gateway-caused 5xx cases.
func TestUpstreamServerErrorsNarrowTheWindowAndClientErrorsDoNot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		status       int
		body         string
		wantTerminal Terminal
		wantVerdict  limits.Verdict
		wantMoves    bool
	}{
		{name: "500", status: 500, wantTerminal: TerminalUpstreamServerError, wantVerdict: limits.UpstreamFault, wantMoves: true},
		{name: "502", status: 502, wantTerminal: TerminalUpstreamServerError, wantVerdict: limits.UpstreamFault, wantMoves: true},
		{name: "504", status: 504, wantTerminal: TerminalUpstreamServerError, wantVerdict: limits.UpstreamFault, wantMoves: true},
		{name: "400", status: 400, wantTerminal: TerminalRejected, wantVerdict: limits.ModelOutcome, wantMoves: false},
		{
			name: "500 on an escrow we never opened", status: 500, body: bridge.ErrEscrowNotFound.Error(),
			wantTerminal: TerminalRejected, wantVerdict: limits.ModelOutcome,
		},
		{
			name: "500 on a nonce we allocated past the cap", status: 500,
			body:         types.ErrNonceLimitExceeded.Error() + ": nonce 21 exceeds active cap 20",
			wantTerminal: TerminalRejected, wantVerdict: limits.ModelOutcome,
		},
		{
			name: "500 on a payload we mismatched", status: 500, body: types.ErrPromptHashMismatch.Error(),
			wantTerminal: TerminalRejected, wantVerdict: limits.ModelOutcome,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := &transport.UpstreamStatusError{Path: "/v1/chat/completions", StatusCode: testCase.status, Body: testCase.body}

			terminal := classifyDispatchError(context.Background(), err)
			if terminal != testCase.wantTerminal {
				t.Fatalf("status %d classified as %v, want %v", testCase.status, terminal, testCase.wantTerminal)
			}

			attempt := failedAttempt(terminal)
			verdict, moves := race(attempt).Verdict(attempt)
			if verdict != testCase.wantVerdict || moves != testCase.wantMoves {
				t.Fatalf("Verdict() = (%v, %v), want (%v, %v)",
					verdict, moves, testCase.wantVerdict, testCase.wantMoves)
			}
		})
	}
}
