package scheduler

import "testing"

// Test flow:
//  1. Build an admission check from a nil trust list and nil unthrottled list.
//  2. Assert every participant tried, including an empty string, is admitted.
func TestTrustEmptyAdmitsEveryone(t *testing.T) {
	t.Parallel()
	allowed := admittedParticipants(nil, nil)

	for _, participant := range []string{"gonka1a", "gonka1b", ""} {
		if !allowed(participant) {
			t.Errorf("%q must be admitted when no trust list is configured", participant)
		}
	}
}

// Test flow:
//  1. Build an admission check from a two-participant trust list.
//  2. Assert both listed participants are admitted.
//  3. Assert an unlisted participant and an empty string are refused.
func TestTrustAdmitsOnlyWhatItNames(t *testing.T) {
	t.Parallel()
	allowed := admittedParticipants([]string{"gonka1scskt", "gonka1f0u3y"}, nil)

	for _, participant := range []string{"gonka1scskt", "gonka1f0u3y"} {
		if !allowed(participant) {
			t.Errorf("%q is on the list and must be admitted", participant)
		}
	}
	for _, participant := range []string{"gonka1other", ""} {
		if allowed(participant) {
			t.Errorf("%q is outside the list and must be refused", participant)
		}
	}
}

// Test flow:
//  1. Build an admission check from a trust list entry padded with surrounding whitespace.
//  2. Assert the trimmed participant is still admitted.
func TestTrustIgnoresSurroundingSpace(t *testing.T) {
	t.Parallel()
	allowed := admittedParticipants([]string{"  gonka1scskt \t"}, nil)

	if !allowed("gonka1scskt") {
		t.Error("a key padded with space must still admit its participant")
	}
}

// Test flow:
//  1. Build an `availability` where every gate would block, including `untrusted`.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockUntrusted`, the first rung, rather than any other gate.
//  4. Assert its ghost reason string is "participant_untrusted".
func TestTrustBurnNamesItself(t *testing.T) {
	t.Parallel()
	blocked := availability{
		untrusted:   func(string) bool { return true },
		pocRequired: func(string) bool { return true },
		congested:   windowFullWhen(always(true)),
		ejected:     func(string) bool { return true },
	}

	reason := blocked.participantBlocked("gonka1other")

	if reason != blockUntrusted {
		t.Fatalf("reason = %v, want blockUntrusted", reason)
	}
	if got := ghostFor[reason].reason(); got != "participant_untrusted" {
		t.Errorf("burn reason = %q, want participant_untrusted", got)
	}
}

// Test flow:
//  1. Build an `availability` where the host is allowed but its window is full.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockWindowFull`, the next rung down.
func TestTrustLetsTheOtherRungsSpeakForAnAdmittedHost(t *testing.T) {
	t.Parallel()
	windowFull := availability{
		untrusted:   func(string) bool { return false },
		pocRequired: func(string) bool { return false },
		congested:   windowFullWhen(always(true)),
		ejected:     func(string) bool { return false },
	}

	if reason := windowFull.participantBlocked("gonka1scskt"); reason != blockWindowFull {
		t.Errorf("reason = %v, want blockWindowFull", reason)
	}
}
