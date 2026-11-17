package registry

import (
	"maps"
	"testing"
)

// Test flow:
//  1. Publish escrow X of model m1 with host A, escrow Y of model m1 with host B, and escrow Z of model m2 with host A again.
//  2. Assert A's share of X is whole: its slots in m2's escrow say nothing about how m1's weight splits.
func TestAHostsShareOfAnEscrowCountsOnlyThatModelsEscrows(t *testing.T) {
	t.Parallel()
	membership := newRecordingMembership()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{
			"X": newFakeSession("hostA", "hostA", "hostA", "hostA"),
			"Y": newFakeSession("hostB", "hostB", "hostB", "hostB"),
			"Z": newFakeSession("hostA", "hostA", "hostA", "hostA"),
		}).open,
		Membership: membership,
		Now:        fixedClock(),
	})
	mustAdd(t, registry, "X", "m1")
	mustAdd(t, registry, "Y", "m1")
	mustAdd(t, registry, "Z", "m2")

	for escrowID, want := range map[string]map[string]float64{"X": {"hostA": 1}, "Y": {"hostB": 1}, "Z": {"hostA": 1}} {
		if got := membership.sharesFor(escrowID); !maps.Equal(got, want) {
			t.Fatalf("SetEscrowMembership(%s) = %v, want %v", escrowID, got, want)
		}
	}
}
