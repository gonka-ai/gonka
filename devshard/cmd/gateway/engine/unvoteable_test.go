package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
)

type unvoteablePoster struct {
	*stubPoster
	unvoteable map[uint64]bool
}

func (p unvoteablePoster) Voteable(nonce uint64) bool { return !p.unvoteable[nonce] }

// Test flow:
//  1. Settle a race with one unsettled attempt on nonce 7 through a poster that calls that nonce unvoteable.
//  2. Assert no vote was posted.
//  3. Assert exactly one event was reported, a skipped action with the reason `TimeoutReasonUnvoteable` and the attempt's own kind and nonce, and no started event.
func TestAnUnvoteableNonceIsReportedAndNeverPosted(t *testing.T) {
	poster := unvoteablePoster{stubPoster: &stubPoster{vote: TimeoutKindRefused}, unvoteable: map[uint64]bool{7: true}}

	events := settleEvents(race(unsettledAttempt()), poster)

	require.Empty(t, poster.posts)
	require.Len(t, events, 1)
	require.Equal(t, TimeoutActionSkipped, events[0].Action)
	require.Equal(t, TimeoutReasonUnvoteable, events[0].Reason)
	require.Equal(t, TimeoutKindRefused, events[0].Kind)
	require.Equal(t, uint64(7), events[0].Nonce)
}

// Test flow:
//  1. Settle a race with two unsettled attempts, nonces 11 and 12, through a poster that calls only nonce 11 unvoteable.
//  2. Assert only nonce 12 was posted, so the verdict is per nonce and a voteable neighbour is voted.
func TestAVoteableNonceBesideAnUnvoteableOneIsStillPosted(t *testing.T) {
	first := unsettledAttempt()
	first.Nonce = 11
	second := unsettledAttempt()
	second.Nonce = 12
	outcome := race(first)
	outcome.Attempts = []AttemptOutcome{first, second}
	poster := unvoteablePoster{stubPoster: &stubPoster{vote: TimeoutKindRefused}, unvoteable: map[uint64]bool{11: true}}

	settleEvents(outcome, poster)

	require.Len(t, poster.posts, 1)
	require.Equal(t, uint64(12), poster.posts[0].nonce)
}

// Test flow:
//  1. Settle a race whose one attempt claims an error miss, through a poster that calls its nonce unvoteable.
//  2. Assert no post was made and the event is skipped as unvoteable, since a miss claim's votes leave out the executor's address too.
func TestAnUnvoteableMissClaimIsNeverPosted(t *testing.T) {
	attempt := unsettledAttempt()
	attempt.MissProof = &MissProof{ResponsePayload: []byte(`{"events":[]}`)}
	steps := race(attempt).TimeoutPlan()
	steps[0].Kind = SettleByMissClaim
	poster := unvoteablePoster{stubPoster: &stubPoster{}, unvoteable: map[uint64]bool{7: true}}
	var events []TimeoutEvent

	settleSteps(context.Background(), poster, steps, false, func(event TimeoutEvent) { events = append(events, event) })

	require.Empty(t, poster.posts)
	require.Len(t, events, 1)
	require.Equal(t, TimeoutReasonUnvoteable, events[0].Reason)
}

// Test flow:
//  1. Build a state machine over a 16-slot group where one validator holds 8 slots, with vote threshold 8.
//  2. Ask `SessionTimeouts.Voteable` about a nonce the unvoteable host executes and one another validator executes.
//  3. Assert that host's nonce is unvoteable and the other is voteable, read from the machine's own group and threshold.
func TestSessionTimeoutsJudgesANonceByTheMachinesOwnGroup(t *testing.T) {
	const unvoteableSlots, groupSize = 8, 16
	signers := make([]*signing.Secp256k1Signer, groupSize-unvoteableSlots+1)
	slotsPerSigner := make([]int, len(signers))
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
		slotsPerSigner[index] = 1
	}
	slotsPerSigner[0] = unvoteableSlots
	group := testutil.MakeMultiSlotGroup(signers, slotsPerSigner)
	config := testutil.DefaultConfig(len(group))
	machine := statetest.MustStateMachine(t, "escrow-1", config, group, 1_000_000, testutil.MustGenerateKey(t).Address(), signing.NewSecp256k1Verifier())
	timeouts := &SessionTimeouts{machine: machine}

	require.Equal(t, uint32(groupSize/2), machine.VoteThreshold())
	require.False(t, timeouts.Voteable(0))
	require.True(t, timeouts.Voteable(unvoteableSlots))
}
