package scheduler

import "testing"

// Test flow:
//  1. Build an admission check from a nil allowlist and nil unthrottled list.
//  2. Assert every participant tried, including an empty string, is admitted.
func TestAllowlistEmptyAdmitsEveryone(t *testing.T) {
	t.Parallel()
	allowed := admittedParticipants(nil, nil)

	for _, participant := range []string{"gonka1a", "gonka1b", ""} {
		if !allowed(participant) {
			t.Errorf("%q must be admitted when no allowlist is configured", participant)
		}
	}
}

// Test flow:
//  1. Build an admission check from a two-participant allowlist.
//  2. Assert both listed participants are admitted.
//  3. Assert an unlisted participant and an empty string are refused.
func TestAllowlistAdmitsOnlyWhatItNames(t *testing.T) {
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
//  1. Build an admission check from an allowlist entry padded with surrounding whitespace.
//  2. Assert the trimmed participant is still admitted.
func TestAllowlistIgnoresSurroundingSpace(t *testing.T) {
	t.Parallel()
	allowed := admittedParticipants([]string{"  gonka1scskt \t"}, nil)

	if !allowed("gonka1scskt") {
		t.Error("a key padded with space must still admit its participant")
	}
}

// Test flow:
//  1. Build an `availability` where every gate would block, including `notAllowed`.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockNotAllowed`, the first rung, rather than any other gate.
//  4. Assert its ghost reason string is "participant_outside_allowlist".
func TestAllowlistBurnNamesItself(t *testing.T) {
	t.Parallel()
	blocked := availability{
		notAllowed:  func(string) bool { return true },
		pocRequired: func(string) bool { return true },
		congested:   windowFullWhen(always(true)),
		ejected:     func(string) bool { return true },
	}

	reason := blocked.participantBlocked("gonka1other")

	if reason != blockNotAllowed {
		t.Fatalf("reason = %v, want blockNotAllowed", reason)
	}
	if got := ghostFor[reason].reason(); got != "participant_outside_allowlist" {
		t.Errorf("burn reason = %q, want participant_outside_allowlist", got)
	}
}

// Test flow:
//  1. Build an `availability` where the host is allowed but its window is full.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockWindowFull`, the next rung down.
func TestAllowlistLetsTheOtherRungsSpeakForAnAdmittedHost(t *testing.T) {
	t.Parallel()
	windowFull := availability{
		notAllowed:  func(string) bool { return false },
		pocRequired: func(string) bool { return false },
		congested:   windowFullWhen(always(true)),
		ejected:     func(string) bool { return false },
	}

	if reason := windowFull.participantBlocked("gonka1scskt"); reason != blockWindowFull {
		t.Errorf("reason = %v, want blockWindowFull", reason)
	}
}
