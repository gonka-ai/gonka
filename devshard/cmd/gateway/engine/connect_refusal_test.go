package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"devshard/cmd/gateway/limits"
)

// Test flow:
//  1. Build a table of Connect refusals a host answers a chat with: a rate limit, an overload, a forbidden caller, an unknown escrow, an unauthenticated caller and an internal fault.
//  2. Classify each, wrapped the way a session wraps a send error.
//  3. Assert each lands on the terminal and verdict its HTTP status would, so a throttling host is backed off rather than cut off as a dead transport.
func TestAConnectRefusalIsClassifiedLikeItsHTTPStatus(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		code         connect.Code
		message      string
		wantTerminal Terminal
		wantVerdict  limits.Verdict
	}{
		{name: "rate limited", code: connect.CodeResourceExhausted, message: "rate limit exceeded", wantTerminal: TerminalThrottled, wantVerdict: limits.Overload},
		{name: "overloaded", code: connect.CodeUnavailable, message: "no capacity", wantTerminal: TerminalUnavailable, wantVerdict: limits.Overload},
		{name: "forbidden", code: connect.CodePermissionDenied, message: "not a member", wantTerminal: TerminalForbidden, wantVerdict: limits.TransportFault},
		{name: "unknown escrow", code: connect.CodeNotFound, message: "session not found", wantTerminal: TerminalNotFound, wantVerdict: limits.TransportFault},
		{name: "unauthenticated", code: connect.CodeUnauthenticated, message: "bad token", wantTerminal: TerminalRejected, wantVerdict: limits.ModelOutcome},
		{name: "timestamp drift", code: connect.CodeUnauthenticated, message: "timestamp drift too large", wantTerminal: TerminalTimestampDrift, wantVerdict: limits.TransportFault},
		{name: "internal", code: connect.CodeInternal, message: "boom", wantTerminal: TerminalUpstreamServerError, wantVerdict: limits.UpstreamFault},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := fmt.Errorf("escrow 1 nonce 7: %w", connect.NewError(testCase.code, errors.New(testCase.message)))

			terminal := classifyDispatchError(context.Background(), err)
			verdict, _ := terminal.verdict()

			if terminal != testCase.wantTerminal || verdict != testCase.wantVerdict {
				t.Fatalf("%v classified as (%v, %v), want (%v, %v)",
					testCase.code, terminal, verdict, testCase.wantTerminal, testCase.wantVerdict)
			}
		})
	}
}

// Test flow:
//  1. Wrap a Connect rate-limit refusal the way a session wraps a send error.
//  2. Assert the client is answered 429 and the refusal's status and message are kept for the log.
func TestAConnectRateLimitAnswersTheClientWithTooManyRequests(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("escrow 1 nonce 7: %w", connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))

	status, body := upstreamRefusal(err)

	if got := StatusForError(err); got != http.StatusTooManyRequests {
		t.Fatalf("StatusForError() = %d, want 429", got)
	}
	if status != http.StatusTooManyRequests || body != "rate limit exceeded" {
		t.Fatalf("upstreamRefusal() = (%d, %q), want (429, \"rate limit exceeded\")", status, body)
	}
}
