package engine

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type overflowRecorder struct {
	cause error
}

func (r *overflowRecorder) record(cause error) { r.cause = cause }

func prefixWriter(budget *carryBudget, participant string, recorder *overflowRecorder, crown chan<- crownRequest, raceDone <-chan struct{}) *winnerWriter {
	return newWinnerWriter(1, &recordingClient{}, crown, raceDone, newPrefixAllowance(budget, participant, recorder.record))
}

// Test flow:
//  1. Write one attempt's pre-content bytes past its own share of the prefix budget.
//  2. Assert the attempt is ended for an over-budget prefix and its bytes are given back.
func TestWinnerWriterEndsAnAttemptPastItsPrefixShare(t *testing.T) {
	budget := testBudget(16, 64, 64)
	recorder := &overflowRecorder{}
	writer := prefixWriter(budget, "host-a", recorder, make(chan crownRequest), nil)

	if err := writer.Write(bytes.Repeat([]byte("p"), 17), false); err != nil {
		t.Fatalf("Write(prefix) = %v", err)
	}

	if !errors.Is(recorder.cause, errPrefixOverBudget) {
		t.Fatalf("overflow cause = %v, want the attempt's prefix over budget", recorder.cause)
	}
	if held := budget.global.Load(); held != 0 {
		t.Fatalf("budget still holds %d bytes, want them given back", held)
	}
}

// Test flow:
//  1. Write two attempts' pre-content bytes on one host, together past the host's share.
//  2. Assert only the second attempt is ended for an over-budget prefix.
func TestWinnerWriterEndsAnAttemptPastItsHostsPrefixShare(t *testing.T) {
	budget := testBudget(16, 24, 64)
	first, second := &overflowRecorder{}, &overflowRecorder{}
	firstWriter := prefixWriter(budget, "host-a", first, make(chan crownRequest), nil)
	secondWriter := prefixWriter(budget, "host-a", second, make(chan crownRequest), nil)

	_ = firstWriter.Write(bytes.Repeat([]byte("p"), 16), false)
	_ = secondWriter.Write(bytes.Repeat([]byte("p"), 16), false)

	if first.cause != nil {
		t.Fatalf("first attempt ended with %v, want it inside the host's share", first.cause)
	}
	if !errors.Is(second.cause, errPrefixOverBudget) {
		t.Fatalf("second attempt cause = %v, want the host's prefix over budget", second.cause)
	}
}

// Test flow:
//  1. Write two attempts' pre-content bytes on two hosts, together past the gateway's budget.
//  2. Assert the second attempt is ended for gateway pressure, which is not the host's fault.
func TestWinnerWriterEndsAnAttemptPastTheGatewaysPrefixBudget(t *testing.T) {
	budget := testBudget(16, 64, 24)
	first, second := &overflowRecorder{}, &overflowRecorder{}
	firstWriter := prefixWriter(budget, "host-a", first, make(chan crownRequest), nil)
	secondWriter := prefixWriter(budget, "host-b", second, make(chan crownRequest), nil)

	_ = firstWriter.Write(bytes.Repeat([]byte("p"), 16), false)
	_ = secondWriter.Write(bytes.Repeat([]byte("p"), 16), false)

	if first.cause != nil {
		t.Fatalf("first attempt ended with %v, want it inside the budget", first.cause)
	}
	if !errors.Is(second.cause, errPrefixGatewayPressure) {
		t.Fatalf("second attempt cause = %v, want gateway pressure", second.cause)
	}
}

// Test flow:
//  1. End the race, then write an attempt's pre-content bytes.
//  2. Assert nothing is charged to the budget: a finished race has no client to flush a prefix to.
func TestWinnerWriterHoldsNoPrefixOnceTheRaceEnds(t *testing.T) {
	budget := testBudget(16, 64, 64)
	recorder := &overflowRecorder{}
	raceDone := make(chan struct{})
	close(raceDone)
	writer := prefixWriter(budget, "host-a", recorder, make(chan crownRequest), raceDone)

	_ = writer.Write(bytes.Repeat([]byte("p"), 10), false)

	if held := budget.global.Load(); held != 0 {
		t.Fatalf("budget holds %d bytes after the race ended, want none", held)
	}
	if recorder.cause != nil {
		t.Fatalf("overflow cause = %v, want none", recorder.cause)
	}
}

// Test flow:
//  1. Write pre-content bytes, then a content chunk that wins the crown, and separately one that loses it.
//  2. Assert the budget is empty afterwards either way: a forwarded or forfeited prefix is given back.
func TestWinnerWriterGivesThePrefixBackOnceItIsSettled(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		verdict streamVerdict
	}{
		{"crowned", streamWinner},
		{"suppressed", streamSuppressed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			guardGoroutines(t)
			budget := testBudget(16, 64, 64)
			desk := newCrownDesk(t, testCase.verdict)
			writer := prefixWriter(budget, "host-a", &overflowRecorder{}, desk.requests, nil)

			_ = writer.Write(bytes.Repeat([]byte("p"), 10), false)
			_ = writer.Write([]byte("content\n\n"), true)

			if held := budget.global.Load(); held != 0 {
				t.Fatalf("budget holds %d bytes, want the prefix given back", held)
			}
		})
	}
}

// Test flow:
//  1. Classify a dispatch whose attempt context was cancelled for an over-budget prefix, then one cancelled for gateway pressure.
//  2. Assert both read as a response too large rather than a client cancel, and only gateway pressure spares the host its perf sample.
func TestAPrefixCancelIsReadAsAResponseTooLarge(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		cause         error
		wantExemption SampleExemption
	}{
		{"host over budget", errPrefixOverBudget, SampleRecorded},
		{"gateway pressure", errPrefixGatewayPressure, ExemptGatewayPressure},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(testCase.cause)

			state := &attemptState{}
			state.classify(ctx, AttemptSpec{}, context.Canceled)

			if state.terminal != TerminalResponseTooLarge {
				t.Fatalf("terminal = %v, want TerminalResponseTooLarge", state.terminal)
			}
			attempt := failedAttempt(TerminalResponseTooLarge)
			attempt.GatewayPressure = state.gatewayPressure
			outcome := RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{attempt}, Succeeded: true}
			if exemption := outcome.sampleExemption(attempt); exemption != testCase.wantExemption {
				t.Fatalf("sample exemption = %v, want %v", exemption, testCase.wantExemption)
			}
		})
	}
}

// Test flow:
//  1. Run a race with one host whose answer opens with more pre-content events than the prefix budget lets one attempt hold, then content.
//  2. Assert the attempt ended as a response too large, the race crowned nobody, and the budget was given back.
func TestRunRaceEndsAnAttemptWhosePrefixOutgrowsTheBudget(t *testing.T) {
	budget := testBudget(int64(len(roleEvent))+1, 1<<20, 1<<20)
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.PrefixBudget = budget
	fixture.host(80, 0, "host-0", &hostScript{
		receipt: true,
		chunks:  []string{roleEvent, roleEvent, contentEvent("late")},
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported

	if terminal := reported.Attempts[0].Terminal; terminal != TerminalResponseTooLarge {
		t.Fatalf("terminal = %v, want TerminalResponseTooLarge", terminal)
	}
	if reported.Succeeded {
		t.Fatal("race succeeded, want the over-budget attempt to serve nothing")
	}
	if held := budget.global.Load(); held != 0 {
		t.Fatalf("prefix budget still holds %d bytes after the over-budget attempt ended", held)
	}
}

// Test flow:
//  1. Run races whose one host buffers a prefix and then ends with no content: an empty stream, and a failed send.
//  2. Assert the prefix budget is empty afterwards: an attempt that never claims still gives back what it held.
func TestAnAttemptEndingWithoutContentGivesItsPrefixBack(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		script *hostScript
	}{
		{"an empty stream", &hostScript{receipt: true, chunks: []string{roleEvent}}},
		{"a failed send", &hostScript{receipt: true, chunks: []string{roleEvent}, err: errors.New("connection reset")}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			budget := testBudget(1<<20, 1<<20, 1<<20)
			fixture := newRaceFixture(settledPolicy(), 1)
			fixture.deps.PrefixBudget = budget
			fixture.host(80, 0, "host-0", testCase.script)

			if _, err := fixture.run(context.Background()); err != nil {
				t.Fatalf("runRace error = %v", err)
			}
			<-fixture.reported

			if held := budget.global.Load(); held != 0 {
				t.Fatalf("prefix budget still holds %d bytes after the attempt ended", held)
			}
		})
	}
}

// Test flow:
//  1. Build the outcome of an attempt the gateway's own prefix pressure ended, and of one its host's oversize prefix ended.
//  2. Assert the pressure is carried onto the outcome, and only it moves no window: the host's own oversize answer still does.
func TestGatewayPressureReachesTheOutcomeAndSparesTheHostsWindows(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		pressure  bool
		wantMoves bool
	}{
		{"the gateway's pressure", true, false},
		{"the host's oversize prefix", false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := &attemptState{terminal: TerminalResponseTooLarge, gatewayPressure: testCase.pressure}

			attempt := *state.outcome(AttemptSpec{Participant: testParticipant, Nonce: fakePrepared{nonce: 77, hostIdx: 3}})

			if attempt.GatewayPressure != testCase.pressure {
				t.Fatalf("outcome GatewayPressure = %v, want %v", attempt.GatewayPressure, testCase.pressure)
			}
			if _, moves := (RaceOutcome{Model: testModel}).Verdict(attempt); moves != testCase.wantMoves {
				t.Fatalf("verdict moves a window = %v, want %v", moves, testCase.wantMoves)
			}
		})
	}
}
