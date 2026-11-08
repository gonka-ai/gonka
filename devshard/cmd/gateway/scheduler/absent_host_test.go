package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Build a scheduler over escrows A and B, both grouping hosts A and B, and report host B absent from escrow A's session.
//  2. Pick two requests on escrow A, so the cursor crosses host B's slot.
//  3. Assert both requests were served by host A and host B's nonce was burned as absent from the chain.
//  4. Assert escrow B's ladder still lets host B serve, so the block stays on the escrow that holds the dead slot.
func TestAHostAbsentFromOneEscrowsSessionBurnsThereAndServesElsewhere(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA, escrowB}})
	test.weights.byEscrow[escrowB] = 0
	test.scheduler.HostAbsent(escrowA, hostB)

	for range 2 {
		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
		require.NoError(t, err)
		require.Equal(t, escrowA, assignment.Escrow)
		require.Equal(t, hostA, assignment.Host)
	}

	require.Contains(t, test.observer.burns(), GhostReasonAbsentFromChain)
	escrowBLadder := test.scheduler.predicates(Escrow{ID: escrowB, Model: modelA, Session: test.session(t, escrowB)})(chain.PhaseSnapshot{})
	require.Equal(t, blockNone, escrowBLadder.participantBlocked(hostB))
}

// Test flow:
//  1. Report host B absent from escrow A's session and assert AbsentHosts lists it under escrow A alone.
//  2. Forget escrow A's absent hosts.
//  3. Assert AbsentHosts is empty and escrow A's ladder lets host B serve again.
func TestForgettingAnEscrowsAbsentHostsLetsTheHostServeThereAgain(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA, escrowB}})
	test.scheduler.HostAbsent(escrowA, hostB)
	require.Equal(t, map[string][]string{escrowA: {hostB}}, test.scheduler.AbsentHosts())

	test.scheduler.ForgetAbsentHosts(escrowA)

	require.Empty(t, test.scheduler.AbsentHosts())
	escrowALadder := test.scheduler.predicates(Escrow{ID: escrowA, Model: modelA, Session: test.session(t, escrowA)})(chain.PhaseSnapshot{})
	require.Equal(t, blockNone, escrowALadder.participantBlocked(hostB))
}
