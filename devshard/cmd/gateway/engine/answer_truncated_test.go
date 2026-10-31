package engine

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/limits"
	"devshard/transport"
)

// Test flow:
//  1. For each host script in the table (content then [DONE], content on a finished nonce, both, and neither, each ended cleanly or by a transfer the transport reports broken), run a one-host race through the engine.
//  2. Assert the error Run returns, the status that error maps to, whether the race succeeded, the winner's terminal, and the verdict the limiter reads from it.
//  3. Assert a winner whose nonce is unfinished is still planned a posted timeout vote.
func TestATruncatedAnswerFailsTheRace(t *testing.T) {
	sseDone := string(filters.SSEDoneEvent)
	testCases := []struct {
		name          string
		chunks        []string
		finished      bool
		transportErr  error
		wantErr       error
		wantStatus    int
		wantSucceeded bool
		wantTerminal  Terminal
		wantVerdict   limits.Verdict
	}{
		{name: "a finished answer", chunks: []string{roleEvent, contentEvent("whole"), sseDone}, finished: true, wantStatus: http.StatusOK, wantSucceeded: true, wantTerminal: TerminalWon, wantVerdict: limits.Success},
		{name: "a terminated answer on an unfinished nonce", chunks: []string{roleEvent, contentEvent("whole"), sseDone}, wantStatus: http.StatusOK, wantSucceeded: true, wantTerminal: TerminalWon, wantVerdict: limits.ModelOutcome},
		{name: "an unterminated answer on a finished nonce", chunks: []string{roleEvent, contentEvent("whole")}, finished: true, wantStatus: http.StatusOK, wantSucceeded: true, wantTerminal: TerminalWon, wantVerdict: limits.Success},
		{name: "a truncated answer", chunks: []string{roleEvent, contentEvent("half")}, wantErr: ErrAnswerTruncated, wantStatus: http.StatusBadGateway, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault},
		{name: "a broken transfer after [DONE] on an unfinished nonce", chunks: []string{roleEvent, contentEvent("whole"), sseDone}, transportErr: transport.ErrSSEStreamTruncated, wantErr: ErrWinnerIncomplete, wantStatus: http.StatusBadGateway, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault},
		{name: "a broken transfer before [DONE] on a finished nonce", chunks: []string{roleEvent, contentEvent("whole")}, finished: true, transportErr: transport.ErrSSEStreamTruncated, wantErr: ErrWinnerIncomplete, wantStatus: http.StatusBadGateway, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault},
		{name: "a broken transfer before [DONE] on an unfinished nonce", chunks: []string{roleEvent, contentEvent("half")}, transportErr: transport.ErrSSEStreamTruncated, wantErr: ErrAnswerTruncated, wantStatus: http.StatusBadGateway, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sim := newSimulator(t, settledPolicy(), 1, qwenModel)
			sim.host(10, 0, "host-0", &hostScript{
				receipt:   true,
				chunks:    testCase.chunks,
				confirmed: true,
				finished:  testCase.finished,
				err:       testCase.transportErr,
			})

			_, err := sim.run(context.Background())
			reported := sim.reported(t)
			sim.settleAll()

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Run() error = %v, want %v", err, testCase.wantErr)
			}
			if status := StatusForError(err); status != testCase.wantStatus {
				t.Errorf("StatusForError(%v) = %d, want %d", err, status, testCase.wantStatus)
			}
			if reported.Succeeded != testCase.wantSucceeded {
				t.Errorf("Succeeded = %t, want %t", reported.Succeeded, testCase.wantSucceeded)
			}
			if len(reported.Attempts) != 1 {
				t.Fatalf("attempts = %d, want 1", len(reported.Attempts))
			}
			winner := reported.Attempts[0]
			if !reported.IsWinner(winner) {
				t.Fatalf("the only attempt %d is not the winner %d", winner.Nonce, reported.WinnerNonce)
			}
			if winner.Terminal != testCase.wantTerminal {
				t.Errorf("winner terminal = %v, want %v", winner.Terminal, testCase.wantTerminal)
			}
			if verdict, _ := reported.Verdict(winner); verdict != testCase.wantVerdict {
				t.Errorf("winner verdict = %v, want %v", verdict, testCase.wantVerdict)
			}
			if testCase.finished {
				return
			}
			plan := reported.TimeoutPlan()
			if len(plan) != 1 || plan[0].Nonce != winner.Nonce || !plan[0].Post {
				t.Errorf("timeout plan = %+v, want one posted vote for nonce %d", plan, winner.Nonce)
			}
		})
	}
}

// Test flow:
//  1. For each loser script (content with or without [DONE], on a finished or unfinished nonce, closed cleanly or by a transfer the transport reports broken), start a two-host race whose first host is held before its receipt.
//  2. Move the clock past the receipt timeout so the race dispatches the loser, which sends its receipt and first chunk and waits.
//  3. Release the first host to answer with [DONE] on a finished nonce, assert the race returns it, then let the loser finish its script.
//  4. Assert the race succeeded with the first host's attempt as TerminalWon.
//  5. Assert the loser's terminal, the verdict the limiter reads from it, and the moves the limiter received for the loser's host.
func TestALoserTheHostCutShortCountsAsATransportFault(t *testing.T) {
	sseDone := string(filters.SSEDoneEvent)
	policy := settledPolicy()
	policy.ReceiptTimeout = time.Second
	policy.MaxAttemptsPerRequest = 2
	testCases := []struct {
		name              string
		loserChunks       []string
		loserFinished     bool
		loserTransportErr error
		wantTerminal      Terminal
		wantVerdict       limits.Verdict
		wantLoserMoves    []windowMove
	}{
		{name: "a loser closed without [DONE] on an unfinished nonce", loserChunks: []string{roleEvent, contentEvent("half")}, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault, wantLoserMoves: []windowMove{{participant: "loser-host", verdict: limits.TransportFault}}},
		{name: "a loser closed after [DONE] on an unfinished nonce", loserChunks: []string{roleEvent, contentEvent("whole"), sseDone}, wantTerminal: TerminalLost, wantVerdict: limits.ModelOutcome},
		{name: "a loser closed without [DONE] on a finished nonce", loserChunks: []string{roleEvent, contentEvent("whole")}, loserFinished: true, wantTerminal: TerminalLost, wantVerdict: limits.Success, wantLoserMoves: []windowMove{{participant: "loser-host", verdict: limits.Success}}},
		{name: "a loser closed after [DONE] on a finished nonce", loserChunks: []string{roleEvent, contentEvent("whole"), sseDone}, loserFinished: true, wantTerminal: TerminalLost, wantVerdict: limits.Success, wantLoserMoves: []windowMove{{participant: "loser-host", verdict: limits.Success}}},
		{name: "a loser's broken transfer before [DONE] on an unfinished nonce", loserChunks: []string{roleEvent, contentEvent("half")}, loserTransportErr: transport.ErrSSEStreamTruncated, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault, wantLoserMoves: []windowMove{{participant: "loser-host", verdict: limits.TransportFault}}},
		{name: "a loser's broken transfer after [DONE] on a finished nonce", loserChunks: []string{roleEvent, contentEvent("whole"), sseDone}, loserFinished: true, loserTransportErr: transport.ErrSSEStreamTruncated, wantTerminal: TerminalStreamTruncated, wantVerdict: limits.TransportFault, wantLoserMoves: []windowMove{{participant: "loser-host", verdict: limits.TransportFault}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sim := newSimulator(t, policy, 2, qwenModel)
			arrive, streaming := make(chan uint64, 1), make(chan uint64, 1)
			winnerRelease, loserResume := make(chan struct{}), make(chan struct{})
			sim.host(10, 0, "winner-host", &hostScript{
				arrive: arrive, release: winnerRelease,
				receipt: true, chunks: []string{roleEvent, contentEvent("whole"), sseDone},
				confirmed: true, finished: true,
			})
			sim.host(11, 1, "loser-host", &hostScript{
				streaming: streaming, resume: loserResume,
				receipt: true, chunks: testCase.loserChunks,
				confirmed: true, finished: testCase.loserFinished,
				err: testCase.loserTransportErr,
			})

			outcomes := runInBackground(t, sim)
			waitForValue(t, arrive, "the first host's dispatch")
			sim.clock.waitArmed(t, policy.ReceiptTimeout)
			sim.clock.advance(policy.ReceiptTimeout)
			waitForValue(t, streaming, "the loser's first chunk")
			close(winnerRelease)
			if served := waitForValue(t, outcomes, "the race's answer"); served.WinnerNonce != 10 {
				t.Fatalf("served winner = %d, want 10", served.WinnerNonce)
			}
			close(loserResume)
			reported := sim.reported(t)
			sim.settleAll()

			if !reported.Succeeded || reported.WinnerNonce != 10 {
				t.Fatalf("winner = %d succeeded %t, want 10/true", reported.WinnerNonce, reported.Succeeded)
			}
			if winner := attemptFor(t, reported, 10); winner.Terminal != TerminalWon {
				t.Errorf("winner terminal = %v, want %v", winner.Terminal, TerminalWon)
			}
			loser := attemptFor(t, reported, 11)
			if loser.Terminal != testCase.wantTerminal {
				t.Errorf("loser terminal = %v, want %v", loser.Terminal, testCase.wantTerminal)
			}
			if verdict, _ := reported.Verdict(loser); verdict != testCase.wantVerdict {
				t.Errorf("loser verdict = %v, want %v", verdict, testCase.wantVerdict)
			}
			var loserMoves []windowMove
			for _, move := range sim.windows.recorded() {
				if move.participant == "loser-host" {
					loserMoves = append(loserMoves, move)
				}
			}
			if !slices.Equal(loserMoves, testCase.wantLoserMoves) {
				t.Errorf("limiter moves for the loser = %+v, want %+v", loserMoves, testCase.wantLoserMoves)
			}
			sim.assertOneOutcome(t)
		})
	}
}
