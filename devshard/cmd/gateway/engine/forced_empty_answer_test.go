package engine

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
)

const (
	forcedEndOfSequenceRoleEvent   = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n"
	forcedEndOfSequenceFinishEvent = `data: {"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}` + "\n\n"
	forcedEndOfSequenceUsageEvent  = `data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}` + "\n\n"
)

func forcedEndOfSequenceAnswer() *hostScript {
	return &hostScript{
		receipt:   true,
		chunks:    []string{forcedEndOfSequenceRoleEvent, forcedEndOfSequenceFinishEvent, forcedEndOfSequenceUsageEvent},
		confirmed: true,
		finished:  true,
	}
}

// Test flow:
//  1. Start a two-host simulator at the default attempt cap of two, where both hosts answer the way an honest host answers a request that forces end-of-sequence first: a role chunk, an empty delta with finish_reason stop, and a usage chunk with one completion token, nonce confirmed and finished.
//  2. Run the race and assert it fails with ErrEmptyStream after dispatching both nonces, the second started because the first failed.
//  3. Assert each attempt reads TerminalEmptyStream and does not judge its host's crowning.
//  4. Assert the window recorder received no move, so the request narrowed neither honest host.
func TestSimulatorARequestForcedEmptyOnTwoHostsJudgesNeither(t *testing.T) {
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	sim.host(10, 0, "honest-host-a", forcedEndOfSequenceAnswer())
	sim.host(11, 1, "honest-host-b", forcedEndOfSequenceAnswer())

	if _, err := sim.run(context.Background()); !errors.Is(err, ErrEmptyStream) {
		t.Fatalf("Run() error = %v, want ErrEmptyStream", err)
	}

	reported := sim.reported(t)
	if len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d, want both nonces dispatched", len(reported.Attempts))
	}
	if escalated := attemptFor(t, reported, 11); escalated.StartReason != EscalationReasonAttemptFailed {
		t.Errorf("second attempt start reason = %q, want %q", escalated.StartReason, EscalationReasonAttemptFailed)
	}
	for _, nonce := range []uint64{10, 11} {
		attempt := attemptFor(t, reported, nonce)
		if attempt.Terminal != TerminalEmptyStream {
			t.Errorf("nonce %d terminal = %v, want %v", nonce, attempt.Terminal, TerminalEmptyStream)
		}
		if reported.JudgesCrowning(attempt) {
			t.Errorf("nonce %d JudgesCrowning = true, want no crown strike for an empty answer another host repeated", nonce)
		}
	}
	if moves := sim.windows.recorded(); len(moves) != 0 {
		t.Fatalf("window moves = %+v, want none for an empty answer another host repeated", moves)
	}
}

// Test flow:
//  1. Start a two-host simulator at the default attempt cap of two and run three races in which both hosts answer a request that forces end-of-sequence first.
//  2. Assert each of the three races fails with ErrEmptyStream after spending two nonces.
//  3. Run a fourth, ordinary race in which honest-host-a has content.
//  4. Assert the fourth race started on honest-host-a alone as an ordinary primary, spent one nonce, and honest-host-a won it.
func TestSimulatorForcedEmptyRequestsLeaveAnHonestHostItsCrown(t *testing.T) {
	const forcedRaces = 3
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	nonce := uint64(10)
	for range forcedRaces {
		sim.host(nonce, 0, "honest-host-a", forcedEndOfSequenceAnswer())
		sim.host(nonce+1, 1, "honest-host-b", forcedEndOfSequenceAnswer())
		nonce += 2
	}
	ordinaryPrimary, ordinaryHedge := nonce, nonce+1
	sim.host(ordinaryPrimary, 0, "honest-host-a", &hostScript{
		receipt: true, chunks: []string{contentEvent("hi")}, confirmed: true, finished: true,
	})
	sim.host(ordinaryHedge, 1, "honest-host-b", forcedEndOfSequenceAnswer())

	for race := range forcedRaces {
		if _, err := sim.run(context.Background()); !errors.Is(err, ErrEmptyStream) {
			t.Fatalf("forced race %d: Run() error = %v, want ErrEmptyStream", race, err)
		}
		if attempts := len(sim.reported(t).Attempts); attempts != 2 {
			t.Fatalf("forced race %d: attempts = %d, want 2", race, attempts)
		}
	}

	outcome, err := sim.run(context.Background())
	if err != nil {
		t.Fatalf("ordinary race: Run() error = %v, want honest-host-a's answer", err)
	}
	if outcome.WinnerNonce != ordinaryPrimary {
		t.Errorf("ordinary race winner = %d, want honest-host-a's nonce %d", outcome.WinnerNonce, ordinaryPrimary)
	}
	reported := sim.reported(t)
	if reported.Decision != StartPrimary {
		t.Errorf("ordinary race decision = %q, want %q: the forced races left honest-host-a crownable", reported.Decision, StartPrimary)
	}
	if len(reported.Attempts) != 1 {
		t.Fatalf("ordinary race attempts = %d, want 1: a crownable primary is not hedged", len(reported.Attempts))
	}
}

// Test flow:
//  1. Start a simulator whose race may start only one attempt, for each of the table's causes: a fleet of one host, and a chain phase that makes nonces scarce while a second host is available.
//  2. Script honest-host-a to answer a request that forces end-of-sequence first, run the race and assert it fails with ErrEmptyStream after dispatching one nonce.
//  3. Assert the attempt reads TerminalEmptyStream, judges and denies its host's crowning, and moved its window with EmptyAnswer, since no second host repeated the empty answer.
func TestSimulatorAForcedEmptyAnswerWithAnAttemptBudgetOfOneIsStillJudged(t *testing.T) {
	cases := []struct {
		name  string
		hosts int
		phase chain.PhaseSnapshot
	}{
		{name: "a_fleet_of_one_host", hosts: 1},
		{name: "nonces_scarce_with_a_second_host_available", hosts: 2, phase: chain.PhaseSnapshot{RequestsBlocked: true, EpochPhase: chain.EpochPhasePoCGenerate}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sim := newSimulatorInPhase(t, speculativePolicy(2), testCase.hosts, qwenModel, config.Modes{}, testCase.phase)
			sim.host(10, 0, "honest-host-a", forcedEndOfSequenceAnswer())
			sim.host(11, 1, "honest-host-b", forcedEndOfSequenceAnswer())

			if _, err := sim.run(context.Background()); !errors.Is(err, ErrEmptyStream) {
				t.Fatalf("Run() error = %v, want ErrEmptyStream", err)
			}

			reported := sim.reported(t)
			if len(reported.Attempts) != 1 {
				t.Fatalf("attempts = %d, want one nonce under an attempt budget of one", len(reported.Attempts))
			}
			attempt := attemptFor(t, reported, 10)
			if attempt.Terminal != TerminalEmptyStream {
				t.Errorf("terminal = %v, want %v", attempt.Terminal, TerminalEmptyStream)
			}
			if !reported.JudgesCrowning(attempt) || !reported.DeniesCrowning(attempt) {
				t.Errorf("JudgesCrowning = %v, DeniesCrowning = %v, want a crown strike for a lone empty answer", reported.JudgesCrowning(attempt), reported.DeniesCrowning(attempt))
			}
			wantMoves := []windowMove{{participant: "honest-host-a", verdict: limits.EmptyAnswer}}
			if moves := sim.windows.recorded(); len(moves) != len(wantMoves) || moves[0] != wantMoves[0] {
				t.Fatalf("window moves = %+v, want %+v", moves, wantMoves)
			}
		})
	}
}

// Test flow:
//  1. Build a race in which empty-host answered empty with its nonce confirmed and finished, varying across the table's cases what else the race held and which hosts finished their nonces.
//  2. Assert Verdict and JudgesCrowning excuse empty-host only when another host also answered empty in a race nobody won and both finished their nonces.
//  3. Assert every other case keeps its judgement: an empty answer in a race a third host served, one that left its nonce open, one whose partner answered empty but left its nonce open, and one whose partner failed without answering empty.
func TestAnEmptyAnswerIsExcusedOnlyWhenAnotherHostRepeatedItInARaceNobodyWon(t *testing.T) {
	emptyFrom := func(participant string, nonce uint64, nonceFinished bool) AttemptOutcome {
		attempt := failedAttempt(TerminalEmptyStream)
		attempt.Participant = participant
		attempt.Nonce = nonce
		attempt.Confirmed = true
		attempt.NonceFinished = nonceFinished
		return attempt
	}
	servedBy := func(participant string, nonce uint64) AttemptOutcome {
		attempt := cleanAttempt()
		attempt.Participant = participant
		attempt.Nonce = nonce
		return attempt
	}
	dialFailureFrom := func(participant string, nonce uint64) AttemptOutcome {
		attempt := failedAttempt(TerminalDialFailure)
		attempt.Participant = participant
		attempt.Nonce = nonce
		return attempt
	}
	cases := []struct {
		name         string
		attempts     []AttemptOutcome
		winnerNonce  uint64
		wantVerdict  limits.Verdict
		wantRecorded bool
		wantJudged   bool
	}{
		{
			name:        "another_host_also_answered_empty_and_nobody_won",
			attempts:    []AttemptOutcome{emptyFrom("empty-host", 10, true), emptyFrom("other-host", 11, true)},
			wantVerdict: limits.ModelOutcome,
		},
		{
			name:         "another_host_also_answered_empty_but_a_third_host_served",
			attempts:     []AttemptOutcome{emptyFrom("empty-host", 10, true), emptyFrom("other-host", 11, true), servedBy("serving-host", 12)},
			winnerNonce:  12,
			wantVerdict:  limits.EmptyAnswer,
			wantRecorded: true,
			wantJudged:   true,
		},
		{
			name:         "another_host_also_answered_empty_but_this_one_left_its_nonce_open",
			attempts:     []AttemptOutcome{emptyFrom("empty-host", 10, false), emptyFrom("other-host", 11, true)},
			wantVerdict:  limits.EmptyAnswerLeftOpen,
			wantRecorded: true,
			wantJudged:   true,
		},
		{
			name:         "the_other_host_answered_empty_but_left_its_nonce_open",
			attempts:     []AttemptOutcome{emptyFrom("empty-host", 10, true), emptyFrom("other-host", 11, false)},
			wantVerdict:  limits.EmptyAnswer,
			wantRecorded: true,
			wantJudged:   true,
		},
		{
			name:         "the_other_host_failed_without_answering_empty",
			attempts:     []AttemptOutcome{emptyFrom("empty-host", 10, true), dialFailureFrom("other-host", 11)},
			wantVerdict:  limits.EmptyAnswer,
			wantRecorded: true,
			wantJudged:   true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := RaceOutcome{
				Model:       testModel,
				WinnerNonce: testCase.winnerNonce,
				Succeeded:   testCase.winnerNonce != 0,
				Attempts:    testCase.attempts,
			}
			judged := outcome.Attempts[0]

			if verdict, recorded := outcome.Verdict(judged); verdict != testCase.wantVerdict || recorded != testCase.wantRecorded {
				t.Errorf("Verdict() = (%v, %v), want (%v, %v)", verdict, recorded, testCase.wantVerdict, testCase.wantRecorded)
			}
			if outcome.JudgesCrowning(judged) != testCase.wantJudged {
				t.Errorf("JudgesCrowning = %v, want %v", outcome.JudgesCrowning(judged), testCase.wantJudged)
			}
		})
	}
}
