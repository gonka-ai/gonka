package scheduler

import "testing"

// Test flow:
//  1. For each table case of a `GhostKind`, call its `reason` method.
//  2. Assert the returned string matches the case's expected reason.
func TestGhostKindReason(t *testing.T) {
	tests := []struct {
		name string
		kind GhostKind
		want string
	}{
		{name: "poc", kind: ghostPoC, want: "poc_unavailable_host"},
		{name: "window full", kind: ghostWindowFull, want: "participant_window_full_no_send"},
		{name: "cut off", kind: ghostCutOff, want: "participant_cut_off_no_send"},
		{name: "state diverged", kind: ghostStateDiverged, want: "participant_state_diverged_no_send"},
		{name: "exclude", kind: ghostExclude, want: "no_compatible_request_after_stale"},
		{name: "prepare failed", kind: ghostPrepareFailed, want: "nonce_spent_on_failed_prepare"},
		{name: "absent from chain", kind: ghostAbsentFromChain, want: "participant_absent_from_chain_no_send"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.kind.reason(); got != tt.want {
				t.Errorf("reason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Test flow:
//  1. List every `GhostKind` the scheduler emits.
//  2. Call `reason` on each and track which reason string has already been seen.
//  3. Assert no kind returns an empty reason.
//  4. Assert no two kinds share the same reason string.
func TestEveryGhostKindNamesItselfDistinctly(t *testing.T) {
	kinds := []GhostKind{
		ghostPoC, ghostWindowFull, ghostCutOff, ghostEjected, ghostNotAllowed,
		ghostStateDiverged, ghostExclude, ghostAbandoned, ghostAbsentFromChain,
	}
	seen := make(map[string]GhostKind, len(kinds))
	for _, kind := range kinds {
		reason := kind.reason()
		if reason == "" {
			t.Errorf("GhostKind(%d) has no reason: a burn nothing can name reaches no counter", kind)
			continue
		}
		if other, taken := seen[reason]; taken {
			t.Errorf("GhostKind(%d) and GhostKind(%d) both report %q", other, kind, reason)
		}
		seen[reason] = kind
	}
}

var (
	_ Decision = serve{}
	_ Decision = burn{}
	_ Decision = hold{}
)
