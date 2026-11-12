package engine

import "testing"

// Test flow:
//  1. Judge one host's escrow-lookup refusals one after another, then one ordinary answer, then a refusal again.
//  2. Assert the first refusals stay excused, the one past the trusted run reads as a throttle, and any other answer restores the trust.
func TestAHostThatOnlyEverRefusesLookupsIsTreatedAsThrottling(t *testing.T) {
	refusals := newLookupRefusals()
	refused := failedAttempt(TerminalEscrowLookupLimited)
	refused.Participant = "host-a"

	judged := make([]Terminal, 0, lookupRefusalsTrusted+1)
	for range lookupRefusalsTrusted + 1 {
		judged = append(judged, refusals.judge(refused, "llama").Terminal)
	}
	answered := failedAttempt(TerminalWon)
	answered.Participant = "host-a"
	refusals.judge(answered, "llama")
	afterAnAnswer := refusals.judge(refused, "llama").Terminal

	for index, terminal := range judged[:lookupRefusalsTrusted] {
		if terminal != TerminalEscrowLookupLimited {
			t.Fatalf("judge(refusal %d) = %v, want TerminalEscrowLookupLimited while trusted", index+1, terminal)
		}
	}
	if last := judged[lookupRefusalsTrusted]; last != TerminalThrottled {
		t.Fatalf("judge(refusal %d) = %v, want TerminalThrottled past the trusted run", lookupRefusalsTrusted+1, last)
	}
	if afterAnAnswer != TerminalEscrowLookupLimited {
		t.Fatalf("judge(refusal after an answer) = %v, want TerminalEscrowLookupLimited, the trust restored", afterAnAnswer)
	}
}
