package scheduler

import (
	"slices"
	"testing"
)

// Test flow:
//  1. Build a 16-slot harness where the shadowed `hostB` holds the first 8 slots and `hostA` the other 8.
//  2. Submit one request and await its reply.
//  3. Assert the request is served by `hostA`, every nonce bound to `hostB` was committed as a probe and none as a ghost, and the prober received each of them.
func TestAShadowHostHoldingHalfTheSlotsStillYieldsItsProbes(t *testing.T) {
	const unvoteableSlots = 8
	slots := slices.Concat(slices.Repeat([]string{hostB}, unvoteableSlots), slices.Repeat([]string{hostA}, unvoteableSlots))
	prober := &recordingProber{}
	test := newHarness(t, harnessConfig{slots: slots, shadowed: shadowing(hostB), prober: prober})

	queued := test.submit(t, test.clock.Now())

	result := awaitReply(t, queued)
	test.dispatcher.stop()
	if result.err != nil || result.assignment.Host != hostA {
		t.Fatalf("reply = %+v, want an assignment on %s", result, hostA)
	}
	_, _, commits := test.session.report()
	var shadowCommits int
	for _, commit := range commits {
		if commit.participant != hostB {
			continue
		}
		shadowCommits++
		if commit.ghost {
			t.Fatalf("commit %+v is a ghost, want a probe for the shadowed host", commit)
		}
	}
	if shadowCommits == 0 || len(prober.probed()) != shadowCommits {
		t.Fatalf("shadow commits = %d, probes = %d, want equal and above zero", shadowCommits, len(prober.probed()))
	}
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none: every shadowed nonce went to a probe", burns)
	}
}
