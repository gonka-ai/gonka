package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Judge lookup-limited attempts from one host past the trusted run so the next one is read as throttled.
//  2. Reset the lookup refusals.
//  3. Assert the next lookup-limited attempt is judged as a refusal again, not as throttled.
func TestResetLookupRefusalsRestartsTheRunOfARefusingHost(t *testing.T) {
	refusals := newLookupRefusals()
	attempt := AttemptOutcome{Participant: "participant-a", Terminal: TerminalEscrowLookupLimited}
	for range lookupRefusalsTrusted + 1 {
		refusals.judge(attempt, "model-a")
	}
	require.Equal(t, TerminalThrottled, refusals.judge(attempt, "model-a").Terminal)

	refusals.reset()

	require.Equal(t, TerminalEscrowLookupLimited, refusals.judge(attempt, "model-a").Terminal)
}
