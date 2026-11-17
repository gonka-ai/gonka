package engine

import (
	"context"
	"fmt"
	"testing"

	"devshard/cmd/gateway/limits"
	"devshard/transport"
)

// Test flow:
//  1. Classify a dispatch error of `transport.ErrHostRequestTooLarge`.
//  2. Assert it maps to `TerminalRequestTooLarge` with reason `ReasonRequestTooLarge`.
//  3. Assert its verdict moves the outcome to the model, not the host.
//  4. Assert a failed attempt with that terminal gets the `ExemptRequestTooLarge` sample exemption.
func TestABodyTheGatewayRefusedToSendIsNotChargedToTheHost(t *testing.T) {
	t.Parallel()

	terminal := classifyDispatchError(context.Background(), transport.ErrHostRequestTooLarge)

	if terminal != TerminalRequestTooLarge {
		t.Fatalf("terminal = %v, want TerminalRequestTooLarge", terminal)
	}
	if terminal.reason() != ReasonRequestTooLarge {
		t.Fatalf("reason = %q, want %q", terminal.reason(), ReasonRequestTooLarge)
	}
	verdict, moves := terminal.verdict()
	if !moves || verdict != limits.ModelOutcome {
		t.Fatalf("verdict = %v (moves %v), want a model outcome: the host's transport is not what refused", verdict, moves)
	}

	outcome := RaceOutcome{Model: testModel, Attempts: []AttemptOutcome{failedAttempt(TerminalRequestTooLarge)}}
	if exemption := outcome.sampleExemption(outcome.Attempts[0]); exemption != ExemptRequestTooLarge {
		t.Fatalf("sample exemption = %v, want ExemptRequestTooLarge", exemption)
	}
}

// Test flow:
//  1. Wrap `transport.ErrHostRequestTooLarge` with escrow and nonce context via `fmt.Errorf`.
//  2. Classify the wrapped error.
//  3. Assert it still resolves to `TerminalRequestTooLarge`.
func TestARefusedBodyIsRecognisedThroughItsWrapping(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("escrow %s nonce %d: %w", "escrow-1", 7, transport.ErrHostRequestTooLarge)

	if got := classifyDispatchError(context.Background(), wrapped); got != TerminalRequestTooLarge {
		t.Fatalf("terminal = %v, want the sentinel to survive wrapping", got)
	}
}
